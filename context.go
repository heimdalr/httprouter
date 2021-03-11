package httprouter

// copy of https://github.com/labstack/echo/blob/4c2fd1fb042b122e2f96830ddb58aee6c9f90bf3/context.go

import (
	"encoding/json"
	"github.com/rs/zerolog"
	"net"
	"net/http"
	"strings"
	"sync"
)

type Context struct {
	Request      *http.Request
	Response     http.ResponseWriter
	Status       int
	Params       Params
	Store        map[string]interface{}
	Logger       zerolog.Logger
	ErrorHandler func(status int, err error, c *Context)
	lock         sync.RWMutex
}

func AcquireContextObject() *Context {
	// TODO: acquire from pool
	return &Context{}
}

func ReleaseContextObject(c *Context) {
	// TODO: release to pool
}

func (c *Context) RealIP() string {
	if ip := c.Request.Header.Get(HeaderXForwardedFor); ip != "" {
		i := strings.IndexAny(ip, ", ")
		if i > 0 {
			return ip[:i]
		}
		return ip
	}
	if ip := c.Request.Header.Get(HeaderXRealIP); ip != "" {
		return ip
	}
	ra, _, _ := net.SplitHostPort(c.Request.RemoteAddr)
	return ra
}

func (c *Context) JSON(code int, i interface{}) error {
	enc := json.NewEncoder(c.Response)
	c.Response.Header().Set(HeaderContentType, MIMEApplicationJSONCharsetUTF8)
	c.Status = code
	c.Response.WriteHeader(code)
	return enc.Encode(i)
}

func (c *Context) JSONPretty(code int, i interface{}, indent string) error {
	enc := json.NewEncoder(c.Response)
	enc.SetIndent("", indent)
	c.Response.Header().Set(HeaderContentType, MIMEApplicationJSONCharsetUTF8)
	c.Status = code
	c.Response.WriteHeader(code)
	return enc.Encode(i)
}

func (c *Context) JSONBlob(code int, b []byte) (err error) {
	c.Response.Header().Set(HeaderContentType, MIMEApplicationJSONCharsetUTF8)
	c.Status = code
	c.Response.WriteHeader(code)
	_, err = c.Response.Write(b)
	return
}

func (c *Context) NoContent(code int) {
	c.Status = code
	c.Response.WriteHeader(code)
}

func (c *Context) Redirect(code int, url string) {
	if code < 300 || code > 308 {
		panic("invalid redirect code")
	}
	c.Response.Header().Set(HeaderLocation, url)
	c.Status = code
	c.Response.WriteHeader(code)
}

func (c *Context) Error(code int, err error) {
	c.ErrorHandler(code, err, c)
}
