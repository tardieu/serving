/*
Copyright 2022 The Knative Authors

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

package store

import (
	"context"

	"github.com/gomodule/redigo/redis"
)

var conn redis.Conn

func Dial(ctx context.Context) (err error) {
	conn, err = redis.DialContext(ctx, "tcp", "redis:6379")
	return err
}

func Close() error {
	return conn.Close()
}

func Set(ctx context.Context, key, value string) (string, error) {
	return redis.String(conn.Do("SET", key, value))
}

func Get(ctx context.Context, key string) (string, error) {
	return redis.String(conn.Do("GET", key))
}

func CAS(ctx context.Context, key, expected, desired string) (bool, error) {
	script := "local v=redis.call('GET', KEYS[1]); if v==ARGV[1] or v==false and ARGV[1]=='' then redis.call('SET', KEYS[1], ARGV[2]); return 1 else return 0 end"
	return redis.Bool(conn.Do("EVAL", script, 1, key, expected, desired))
}
