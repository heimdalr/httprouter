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



type (

	// NyContext represents the myContext of the current HTTP request.
	NyContext interface {

		// JSON sends a JSON response with status code.
		JSON(code int, i interface{}) error

		// JSONPretty sends a pretty-print JSON with status code.
		JSONPretty(code int, i interface{}, indent string) error

		// JSONBlob sends a JSON blob response with status code.
		JSONBlob(code int, b []byte) error

		// NoContent sends a response with no body and a status code.
		NoContent(code int) error

		// Redirect redirects the request to a provided URL with status code.
		Redirect(code int, url string) error

		// Error invokes the registered HTTP error handler. Generally used by middleware.
		Error(err error)

		// Reset resets the myContext after request completes. It must be called along
		// with `Echo#AcquireContext()` and `Echo#ReleaseContext()`.
		// See `Echo#ServeHTTP()`
		Reset(r *http.Request, w http.ResponseWriter)
	}

	myContext struct {
		Request  *http.Request
		Response http.ResponseWriter
		Status   int
		Params   Params
		Store    map[string]interface{}
		Logger   zerolog.Logger
		ErrorHandler func(status int, err error, c *myContext)
		lock     sync.RWMutex
	}
)

func AcquireContextObject() *myContext {
	// TODO: acquire from pool
	return &myContext{}
}

func ReleaseContextObject(c *myContext) {
	// TODO: release to pool
}


func (c *myContext) RealIP() string {
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

func (c *myContext) JSON(code int, i interface{}) error {
	enc := json.NewEncoder(c.Response)
	c.Response.Header().Set(HeaderContentType, MIMEApplicationJSONCharsetUTF8)
	c.Status = code
	c.Response.WriteHeader(code)
	return enc.Encode(i)
}

func (c *myContext) JSONPretty(code int, i interface{}, indent string) error {
	enc := json.NewEncoder(c.Response)
	enc.SetIndent("", indent)
	c.Response.Header().Set(HeaderContentType, MIMEApplicationJSONCharsetUTF8)
	c.Status = code
	c.Response.WriteHeader(code)
	return enc.Encode(i)
}

func (c *myContext) JSONBlob(code int, b []byte) (err error) {
	c.Response.Header().Set(HeaderContentType, MIMEApplicationJSONCharsetUTF8)
	c.Status = code
	c.Response.WriteHeader(code)
	_, err = c.Response.Write(b)
	return
}

func (c *myContext) NoContent(code int) {
	c.Status = code
	c.Response.WriteHeader(code)
}

func (c *myContext) Redirect(code int, url string) {
	if code < 300 || code > 308 {
		panic("invalid redirect code")
	}
	c.Response.Header().Set(HeaderLocation, url)
	c.Status = code
	c.Response.WriteHeader(code)
}

func (c *myContext) Error(code int, err error) {
	c.ErrorHandler(code, err, c)
}
