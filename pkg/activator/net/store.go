package net

import (
	"context"

	"github.com/gomodule/redigo/redis"
)

var StoreNil = redis.ErrNil

var conn redis.Conn

func StoreDial(ctx context.Context) (err error) {
	conn, err = redis.DialContext(ctx, "tcp", "redis:6379")
	return err
}

func StoreClose() error {
	return conn.Close()
}

func StoreSet(ctx context.Context, key, value string) (string, error) {
	return redis.String(conn.Do("SET", key, value))
}

func StoreGet(ctx context.Context, key string) (string, error) {
	return redis.String(conn.Do("GET", key))
}

func StoreDel(ctx context.Context, key, value string) (int, error) {
	return redis.Int(conn.Do("EVAL", "if redis.call('GET', KEYS[1]) == ARGV[1] then redis.call('DEL', KEYS[1]); return 1 else return 0 end", 1, key, value))
}
