/*
Copyright 2020 The Knative Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package handler

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"go.uber.org/zap"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	"knative.dev/pkg/logging"
	"knative.dev/pkg/logging/logkey"
	network "knative.dev/pkg/network"
	"knative.dev/serving/pkg/activator"
	activatorconfig "knative.dev/serving/pkg/activator/config"
	"knative.dev/serving/pkg/activator/store"
	"knative.dev/serving/pkg/apis/serving"
	revisioninformer "knative.dev/serving/pkg/client/injection/informers/serving/v1/revision"
	serviceinformer "knative.dev/serving/pkg/client/injection/informers/serving/v1/service"
	servinglisters "knative.dev/serving/pkg/client/listers/serving/v1"
)

// NewContextHandler creates a handler that extracts the necessary context from the request
// and makes it available on the request's context.
func NewContextHandler(ctx context.Context, next http.Handler, store *activatorconfig.Store) http.Handler {
	return &contextHandler{
		nextHandler:    next,
		revisionLister: revisioninformer.Get(ctx).Lister(),
		serviceLister:  serviceinformer.Get(ctx).Lister(),
		logger:         logging.FromContext(ctx),
		store:          store,
	}
}

// contextHandler enriches the request's context with structured data.
type contextHandler struct {
	revisionLister servinglisters.RevisionLister
	serviceLister  servinglisters.ServiceLister
	logger         *zap.SugaredLogger
	nextHandler    http.Handler
	store          *activatorconfig.Store
}

func (h *contextHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	namespace := r.Header.Get(activator.RevisionHeaderNamespace)
	name := r.Header.Get(activator.RevisionHeaderName)

	// If the headers aren't explicitly specified, then decode the revision
	// name and namespace from the Host header.
	if name == "" || namespace == "" {
		parts := strings.SplitN(r.Host, ".", 4)
		if len(parts) == 4 && parts[2] == "svc" && strings.SplitN(parts[3], ":", 2)[0] == network.GetClusterDomainName() {
			name, namespace = parts[0], parts[1]
		}
	}

	revID := types.NamespacedName{Namespace: namespace, Name: name}

	revision, err := h.revisionLister.Revisions(namespace).Get(name)
	if err != nil {
		h.logger.Errorw("Error while getting revision", zap.String(logkey.Key, revID.String()), zap.Error(err))
		sendError(err, w)
		return
	}

	serviceName := revision.Labels[serving.ServiceLabelKey]
	service, err := h.serviceLister.Services(namespace).Get(serviceName)
	if err != nil {
		h.logger.Errorw("Error while getting service", zap.String(logkey.Key, serviceName), zap.Error(err))
		sendError(err, w)
		return
	}

	session := getSession(r, service.Annotations)
	if session != "" {
		v := ""
		for {
			v, _ = store.CAS(r.Context(), "rev/"+session, v, revID.String())
			if v == revID.String() {
				break
			}
			parts := strings.SplitN(v, string(types.Separator), 2)
			namespace := parts[0]
			name := parts[1]
			stickyRevID := types.NamespacedName{Namespace: namespace, Name: name}
			stickyRevision, err := h.revisionLister.Revisions(namespace).Get(name)
			if err == nil {
				h.logger.Infof("Overriding revision %s with %s", revID.String(), stickyRevID.String())
				revID = stickyRevID
				revision = stickyRevision
				break
			}
			h.logger.Warnw("Error while getting sticky revision", zap.String(logkey.Key, v), zap.Error(err))
		}
	}

	ctx := r.Context()
	ctx = WithRevisionAndID(ctx, revision, revID)
	ctx = h.store.ToContext(ctx)
	h.nextHandler.ServeHTTP(w, r.WithContext(ctx))
}

func sendError(err error, w http.ResponseWriter) {
	msg := fmt.Sprint("Error getting active endpoint: ", err)
	if k8serrors.IsNotFound(err) {
		http.Error(w, msg, http.StatusNotFound)
		return
	}
	http.Error(w, msg, http.StatusInternalServerError)
}

const stickySessionHeaderName = "K-Session"
const stickyRevisionHeaderName = "K-Revision"

func addStickySessionHeader(r *http.Request, session string) string {
	if session != "" {
		r.Header.Add(stickySessionHeaderName, session)
	}
	return session
}

func getSession(r *http.Request, annotations map[string]string) string {
	if p := annotations["activator.knative.dev/deactivate"]; p != "" {
		r.Header.Add("K-Deactivate", p)
	}

	if session := r.Header.Get(stickySessionHeaderName); session != "" {
		return session
	}

	if p := annotations["activator.knative.dev/sticky-session-header-name"]; p != "" {
		return addStickySessionHeader(r, r.Header.Get(p))
	}

	if p := annotations["activator.knative.dev/sticky-session-query-parameter"]; p != "" {
		return addStickySessionHeader(r, r.URL.Query().Get(p))
	}

	if p := annotations["activator.knative.dev/sticky-session-path-segment"]; p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
			if n < len(parts) {
				return addStickySessionHeader(r, parts[n])
			}
		}
	}

	if p := annotations["activator.knative.dev/sticky-revision-header-name"]; p != "" {
		return r.Header.Get(p)
	}

	if p := annotations["activator.knative.dev/sticky-revision-query-parameter"]; p != "" {
		return r.URL.Query().Get(p)
	}

	if p := annotations["activator.knative.dev/sticky-revision-path-segment"]; p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
			if n < len(parts) {
				return parts[n]
			}
		}
	}

	return r.Header.Get(stickyRevisionHeaderName)
}
