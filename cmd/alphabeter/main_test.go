package main

import "testing"

func envOf(vars map[string]string) func(string) string {
	return func(key string) string { return vars[key] }
}

func TestListenAddr(t *testing.T) {
	cases := []struct {
		env  map[string]string
		want string
	}{
		{nil, ":8090"},
		{map[string]string{"PORT": "3000"}, ":3000"},
		{map[string]string{"ADDR": "127.0.0.1:9000", "PORT": "3000"}, "127.0.0.1:9000"},
	}
	for _, c := range cases {
		if got := listenAddr(envOf(c.env)); got != c.want {
			t.Errorf("listenAddr(%v) = %q, want %q", c.env, got, c.want)
		}
	}
}

func TestTrustProxy(t *testing.T) {
	cases := []struct {
		env  map[string]string
		want bool
	}{
		{nil, false},
		{map[string]string{"RAILWAY_PROJECT_ID": "abc"}, true},
		{map[string]string{"RAILWAY_PROJECT_ID": "abc", "TRUST_PROXY": "false"}, false},
		{map[string]string{"TRUST_PROXY": "true"}, true},
	}
	for _, c := range cases {
		if got := trustProxy(envOf(c.env)); got != c.want {
			t.Errorf("trustProxy(%v) = %v, want %v", c.env, got, c.want)
		}
	}
}
