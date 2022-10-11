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

func Del(ctx context.Context, key, value string) (int, error) {
	return redis.Int(conn.Do("EVAL", "if redis.call('GET', KEYS[1]) == ARGV[1] then redis.call('DEL', KEYS[1]); return 1 else return 0 end", 1, key, value))
}
