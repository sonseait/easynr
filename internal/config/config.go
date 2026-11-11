package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

type Config struct{ Data, Listen, PublicURL string }

func Parse(args []string) (Config, error) {
	c := Config{Data: "./data"}
	if len(args) != 0 {
		return c, fmt.Errorf("configuration arguments are not supported")
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "4873"
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 || strings.Trim(port, "0123456789") != "" {
		return c, fmt.Errorf("PORT must be between 1 and 65535")
	}
	c.Listen = "0.0.0.0:" + strconv.Itoa(n)
	c.PublicURL = strings.TrimRight(os.Getenv("PUBLIC_URL"), "/")
	if c.PublicURL != "" {
		u, e := url.Parse(c.PublicURL)
		if e != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
			return c, fmt.Errorf("PUBLIC_URL must be an http(s) origin without path, credentials, query or fragment")
		}
	}
	return c, nil
}
