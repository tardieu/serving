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

// This file contains the load load balancing policies for Activator load balancing.

package net

import (
	"context"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"knative.dev/serving/pkg/activator/store"
)

// lbPolicy is a functor that selects a target pod from the list, or (noop, nil) if
// no such target can be currently acquired.
// Policies will presume that `targets` list is appropriately guarded by the caller,
// that is while podTrackers themselves can change during this call, the list
// and pointers therein are immutable.
type lbPolicy func(ctx context.Context, targets []*podTracker) (func(), *podTracker)

// randomLBPolicy is a load balancer policy that picks a random target.
// This approximates the LB policy done by K8s Service (IPTables based).
//
// nolint // This is currently unused but kept here for posterity.
func randomLBPolicy(_ context.Context, targets []*podTracker) (func(), *podTracker) {
	return noop, targets[rand.Intn(len(targets))]
}

// randomChoice2Policy implements the Power of 2 choices LB algorithm
func randomChoice2Policy(ctx context.Context, targets []*podTracker) (func(), *podTracker) {
	if pick := getSession(ctx, targets); pick != nil {
		return noop, pick
	}

	// Avoid random if possible.
	l := len(targets)
	// One tracker = no choice.
	if l == 1 {
		pick := targets[0]
		if !setSession(ctx, pick) {
			return noop, nil
		}
		pick.increaseWeight()
		return pick.decreaseWeight, pick
	}
	r1, r2 := 0, 1
	// Two trackers - we know both contestants,
	// otherwise pick 2 random unequal integers.
	if l > 2 {
		r1, r2 = rand.Intn(l), rand.Intn(l-1) //nolint:gosec // We don't need cryptographic randomness here.
		// shift second half of second rand.Intn down so we're picking
		// from range of numbers other than r1.
		// i.e. rand.Intn(l-1) range is now from range [0,r1),[r1+1,l).
		if r2 >= r1 {
			r2++
		}
	}

	pick, alt := targets[r1], targets[r2]
	// Possible race here, but this policy is for CC=0,
	// so fine.
	if pick.getWeight() > alt.getWeight() {
		pick = alt
	}
	if !setSession(ctx, pick) {
		return noop, nil
	}
	pick.increaseWeight()
	return pick.decreaseWeight, pick
}

// firstAvailableLBPolicy is a load balancer policy, that picks the first target
// that has capacity to serve the request right now.
func firstAvailableLBPolicy(ctx context.Context, targets []*podTracker) (func(), *podTracker) {
	if pick := getSession(ctx, targets); pick != nil {
		return noop, pick
	}

	for _, t := range targets {
		if cb, ok := t.Reserve(ctx); ok {
			if !setSession(ctx, t) {
				cb()
				return noop, nil
			}
			return cb, t
		}
	}
	return noop, nil
}

func newRoundRobinPolicy() lbPolicy {
	var (
		mu  sync.Mutex
		idx int
	)
	return func(ctx context.Context, targets []*podTracker) (func(), *podTracker) {
		if pick := getSession(ctx, targets); pick != nil {
			return noop, pick
		}

		mu.Lock()
		defer mu.Unlock()

		// The number of trackers might have shrunk, so reset to 0.
		l := len(targets)
		if idx >= l {
			idx = 0
		}

		// Now for |targets| elements and check every next one in
		// round robin fashion.
		for i := 0; i < l; i++ {
			p := (idx + i) % l
			if cb, ok := targets[p].Reserve(ctx); ok {
				if !setSession(ctx, targets[p]) {
					cb()
					return noop, nil
				}
				// We want to start with the next index.
				idx = p + 1
				return cb, targets[p]
			}
		}
		// We exhausted all the options...
		return noop, nil
	}
}

type pair struct {
	Request     *http.Request
	Annotations map[string]string
}

// private key type to attach to context
type key struct{}

// attach request and rev annotations to context
func WithRequestAndAnnotations(ctx context.Context, r *http.Request, a map[string]string) context.Context {
	return context.WithValue(ctx, key{}, pair{r, a})
}

// get session from context
func sessionFrom(ctx context.Context) string {
	p := ctx.Value(key{}).(pair)
	request := p.Request
	annotations := p.Annotations

	if session := request.Header.Get("K-Session"); session != "" {
		return session
	}

	if p := annotations["activator.knative.dev/session-header"]; p != "" {
		return request.Header.Get(p)
	}

	if p := annotations["activator.knative.dev/session-query"]; p != "" {
		return request.URL.Query().Get(p)
	}

	if p := annotations["activator.knative.dev/session-path"]; p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			parts := strings.Split(strings.TrimPrefix(request.URL.Path, "/"), "/")
			if n < len(parts) {
				return parts[n]
			}
		}
	}

	return ""
}

// get pod for session
func getSession(ctx context.Context, targets []*podTracker) *podTracker {
	if session := sessionFrom(ctx); session != "" {
		dest, _ := store.Get(ctx, session)
		if dest != "" {
			for _, t := range targets {
				if dest == t.dest {
					return t
				}
			}
		}
		store.Del(ctx, session, dest)
	}
	return nil
}

// set pod for session
func setSession(ctx context.Context, pick *podTracker) bool {
	if session := sessionFrom(ctx); session != "" {
		if dest, _ := store.Get(ctx, session); dest != "" && dest != pick.dest {
			return false
		}
		store.Set(ctx, session, pick.dest)
	}
	return true
}
