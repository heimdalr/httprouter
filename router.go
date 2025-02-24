// Copyright 2013 Julien Schmidt. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be found
// in the LICENSE file.

// Package httprouter is a trie based high performance HTTP request router.
//
// A trivial example is:
//
//  package main
//
//  import (
//      "fmt"
//      "github.com/julienschmidt/httprouter"
//      "net/http"
//      "log"
//  )
//
//  func Index(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
//      fmt.Fprint(w, "Welcome!\n")
//  }
//
//  func Hello(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
//      fmt.Fprintf(w, "hello, %s!\n", ps.ByName("name"))
//  }
//
//  func main() {
//      router := httprouter.New()
//      router.GET("/", Index)
//      router.GET("/hello/:name", Hello)
//
//      log.Fatal(http.ListenAndServe(":8080", router))
//  }
//
// The router matches incoming requests by the request method and the path.
// If a handle is registered for this path and method, the router delegates the
// request to that function.
// For the methods GET, POST, PUT, PATCH, DELETE and OPTIONS shortcut functions exist to
// register handles, for all other methods router.Handle can be used.
//
// The registered path, against which the router matches incoming requests, can
// contain two types of parameters:
//  Syntax    Type
//  :name     named parameter
//  *name     catch-all parameter
//
// Named parameters are dynamic path segments. They match anything until the
// next '/' or the path end:
//  Path: /blog/:category/:post
//
//  Requests:
//   /blog/go/request-routers            match: category="go", post="request-routers"
//   /blog/go/request-routers/           no match, but the router would redirect
//   /blog/go/                           no match
//   /blog/go/request-routers/comments   no match
//
// Catch-all parameters match anything until the path end, including the
// directory index (the '/' before the catch-all). Since they match anything
// until the end, catch-all parameters must always be the final path element.
//  Path: /files/*filepath
//
//  Requests:
//   /files/                             match: filepath="/"
//   /files/LICENSE                      match: filepath="/LICENSE"
//   /files/templates/article.html       match: filepath="/templates/article.html"
//   /files                              no match, but the router would redirect
//
// The value of parameters is saved as a slice of the Param struct, consisting
// each of a key and a value. The slice is passed to the Handle func as a third
// parameter.
// There are two ways to retrieve the value of a parameter:
//  // by the name of the parameter
//  user := ps.ByName("user") // defined by :user or *user
//
//  // by the index of the parameter. This way you can also get the name (key)
//  thirdKey   := ps[2].Key   // the name of the 3rd parameter
//  thirdValue := ps[2].Value // the value of the 3rd parameter
package httprouter

import (
	"context"
	"encoding/json"
	"github.com/rs/zerolog/log"
	"net/http"
	"strings"
	"sync"
)

// Handle is a function that can be registered to a route to handle HTTP
// requests. Like http.HandlerFunc, but with a context object wrapping request,
// response, params, etc.
type Handle func(c *Context)

// Param is a single URL parameter, consisting of a key and a value.
type Param struct {
	Key   string
	Value string
}

// Params is a Param-slice, as returned by the router.
// The slice is ordered, the first URL parameter is also the first slice value.
// It is therefore safe to read values by the index.
type Params []Param

// ByName returns the value of the first Param which key matches the given name.
// If no matching Param is found, an empty string is returned.
func (ps Params) ByName(name string) string {
	for _, p := range ps {
		if p.Key == name {
			return p.Value
		}
	}
	return ""
}

type paramsKey struct{}

// ParamsKey is the request context key under which URL params are stored.
var ParamsKey = paramsKey{}

// ParamsFromContext pulls the URL parameters from a request context,
// or returns nil if none are present.
func ParamsFromContext(ctx context.Context) Params {
	p, _ := ctx.Value(ParamsKey).(Params)
	return p
}

// MatchedRoutePathParam is the Param name under which the path of the matched
// route is stored, if Router.SaveMatchedRoutePath is set.
var MatchedRoutePathParam = "$matchedRoutePath"

// MatchedRoutePath retrieves the path of the matched route.
// Router.SaveMatchedRoutePath must have been enabled when the respective
// handler was added, otherwise this function always returns an empty string.
func (ps Params) MatchedRoutePath() string {
	return ps.ByName(MatchedRoutePathParam)
}

// Router is a http.Handler which can be used to dispatch requests to different
// handler functions via configurable routes
type Router struct {
	trees map[string]*node

	paramsPool sync.Pool
	maxParams  uint16

	// If enabled, adds the matched route path onto the http.Request Context
	// before invoking the handler.
	// The matched route path is only added to handlers of routes that were
	// registered when this option was enabled.
	SaveMatchedRoutePath bool

	// Enables automatic redirection if the current route can't be matched but a
	// handler for the path with (without) the trailing slash exists.
	// For example if /foo/ is requested but a route only exists for /foo, the
	// client is redirected to /foo with http status code 301 for GET requests
	// and 308 for all other request methods.
	RedirectTrailingSlash bool

	// If enabled, the router tries to fix the current request path, if no
	// handle is registered for it.
	// First superfluous path elements like ../ or // are removed.
	// Afterwards the router does a case-insensitive lookup of the cleaned path.
	// If a handle can be found for this route, the router makes a redirection
	// to the corrected path with status code 301 for GET requests and 308 for
	// all other request methods.
	// For example /FOO and /..//Foo could be redirected to /foo.
	// RedirectTrailingSlash is independent of this option.
	RedirectFixedPath bool

	// If enabled, the router automatically replies to OPTIONS requests.
	// Path-specific OPTIONS handlers take priority over "automatic" replies.
	//
	// Note, if HandleOptions is set to true and Options is not set, the default,
	// very permissive OPTIONS handler will be used.
	//
	// Default: false
	HandleOptions bool

	// A callback function for handling OPTION requests. The function is only called,
	// if no path-specific OPTIONS handler was set and HandleOptions is true.
	//
	// The final parameter of the callback function (i.e. the string parameter) will
	// be populated with a sting describing the methods allowed on the specific path.
	Options func(http.ResponseWriter, *http.Request, string)

	// If enabled, the router checks if another method is allowed for the
	// current route, if the current request can not be routed.
	// If this is the case, the request is answered with 'Method Not Allowed'
	// and HTTP status code 405.
	// If no other Method is allowed, the request is delegated to the NotFound
	// handler.
	// Default: false
	HandleMethodNotAllowed bool

	// A callback function for handling non-OPTION requests. The function is only called,
	// if no path-specific method handler was set and HandleMethodNotAllowed is true.
	//
	// The final parameter of the callback function (i.e. the string parameter) will
	// be populated with a sting describing the methods allowed on the specific path.
	MethodNotAllowed func(http.ResponseWriter, *http.Request, string)

	// A callback function which is called, if no matching route is
	// found. If not set, http.NotFound is used.
	NotFound func(http.ResponseWriter, *http.Request)

	// Function to handle panics recovered from http handlers.
	// It should be used to generate a error page and return the http error code
	// 500 (Internal Server Error).
	// The handler can be used to keep your server from crashing because of
	// unrecovered panics.
	PanicHandler func(http.ResponseWriter, *http.Request, interface{})

	// Cached value of global (*) allowed methods
	globalAllowed string

}

// Make sure the Router conforms with the http.Handler interface
var _ http.Handler = New()

// New returns a new initialized Router.
// Path auto-correction, including trailing slashes, is enabled by default.
func New() *Router {
	return &Router{
		RedirectTrailingSlash:  true,
		RedirectFixedPath:      true,
		HandleOptions: 			false,
		HandleMethodNotAllowed: false,
	}
}

func (r *Router) getParams() *Params {
	ps, _ := r.paramsPool.Get().(*Params)
	*ps = (*ps)[0:0] // reset slice
	return ps
}

func (r *Router) putParams(ps *Params) {
	if ps != nil {
		r.paramsPool.Put(ps)
	}
}

func (r *Router) saveMatchedRoutePath(path string, handle Handle) Handle {
	return func(c *Context) {
		if c.Params == nil {
			psp := r.getParams()
			ps := (*psp)[0:1]
			ps[0] = Param{Key: MatchedRoutePathParam, Value: path}
			c.Params = ps
			handle(c)
			r.putParams(psp)
		} else {
			c.Params = append(c.Params, Param{Key: MatchedRoutePathParam, Value: path})
			handle(c)
		}
	}
}

// GET is a shortcut for router.Handle(http.MethodGet, path, handle)
func (r *Router) GET(path string, handle Handle) {
	r.Handle(http.MethodGet, path, handle)
}

// HEAD is a shortcut for router.Handle(http.MethodHead, path, handle)
func (r *Router) HEAD(path string, handle Handle) {
	r.Handle(http.MethodHead, path, handle)
}

// OPTIONS is a shortcut for router.Handle(http.MethodOptions, path, handle)
func (r *Router) OPTIONS(path string, handle Handle) {
	r.Handle(http.MethodOptions, path, handle)
}

// POST is a shortcut for router.Handle(http.MethodPost, path, handle)
func (r *Router) POST(path string, handle Handle) {
	r.Handle(http.MethodPost, path, handle)
}

// PUT is a shortcut for router.Handle(http.MethodPut, path, handle)
func (r *Router) PUT(path string, handle Handle) {
	r.Handle(http.MethodPut, path, handle)
}

// PATCH is a shortcut for router.Handle(http.MethodPatch, path, handle)
func (r *Router) PATCH(path string, handle Handle) {
	r.Handle(http.MethodPatch, path, handle)
}

// DELETE is a shortcut for router.Handle(http.MethodDelete, path, handle)
func (r *Router) DELETE(path string, handle Handle) {
	r.Handle(http.MethodDelete, path, handle)
}

// Handle registers a new request handle with the given path and method.
//
// For GET, POST, PUT, PATCH and DELETE requests the respective shortcut
// functions can be used.
//
// This function is intended for bulk loading and to allow the usage of less
// frequently used, non-standardized or custom methods (e.g. for internal
// communication with a proxy).
func (r *Router) Handle(method, path string, handle Handle) {
	varsCount := uint16(0)

	if method == "" {
		panic("method must not be empty")
	}
	if len(path) < 1 || path[0] != '/' {
		panic("path must begin with '/' in path '" + path + "'")
	}
	if handle == nil {
		panic("handle must not be nil")
	}

	if r.SaveMatchedRoutePath {
		varsCount++
		handle = r.saveMatchedRoutePath(path, handle)
	}

	if r.trees == nil {
		r.trees = make(map[string]*node)
	}

	root := r.trees[method]
	if root == nil {
		root = new(node)
		r.trees[method] = root

		r.globalAllowed = r.allowed("*", "")
	}

	root.addRoute(path, handle)

	// Update maxParams
	if paramsCount := countParams(path); paramsCount+varsCount > r.maxParams {
		r.maxParams = paramsCount + varsCount
	}

	// Lazy-init paramsPool alloc func
	if r.paramsPool.New == nil && r.maxParams > 0 {
		r.paramsPool.New = func() interface{} {
			ps := make(Params, 0, r.maxParams)
			return &ps
		}
	}
}

// ServeFiles serves files from the given file system root.
// The path must end with "/*filepath", files are then served from the local
// path /defined/root/dir/*filepath.
// For example if root is "/etc" and *filepath is "passwd", the local file
// "/etc/passwd" would be served.
// Internally a http.FileServer is used, therefore http.NotFound is used instead
// of the Router's NotFound handler.
// To use the operating system's file system implementation,
// use http.Dir:
//     router.ServeFiles("/src/*filepath", http.Dir("/var/www"))
func (r *Router) ServeFiles(path string, root http.FileSystem) {
	if len(path) < 10 || path[len(path)-10:] != "/*filepath" {
		panic("path must end with /*filepath in path '" + path + "'")
	}

	fileServer := http.FileServer(root)

	//r.GET(path, func(w http.ResponseWriter, req *http.Request, ps Params) {
	r.GET(path, func(c *Context) {
		c.Request.URL.Path = c.Params.ByName("filepath")
		fileServer.ServeHTTP(c.Response, c.Request)
	})
}

func (r *Router) recv(w http.ResponseWriter, req *http.Request) {
	if rcv := recover(); rcv != nil {
		r.PanicHandler(w, req, rcv)
	}
}

// Lookup allows the manual lookup of a method + path combo.
// This is e.g. useful to build a framework around this router.
// If the path was found, it returns the handle function and the path parameter
// values. Otherwise the third return value indicates whether a redirection to
// the same path with an extra / without the trailing slash should be performed.
func (r *Router) Lookup(method, path string) (Handle, Params, bool) {
	if root := r.trees[method]; root != nil {
		handle, ps, tsr := root.getValue(path, r.getParams)
		if handle == nil {
			r.putParams(ps)
			return nil, nil, tsr
		}
		if ps == nil {
			return handle, nil, tsr
		}
		return handle, *ps, tsr
	}
	return nil, nil, false
}

func (r *Router) allowed(path, reqMethod string) (allow string) {
	allowed := make([]string, 0, 9)

	if path == "*" { // server-wide
		// empty method is used for internal calls to refresh the cache
		if reqMethod == "" {
			for method := range r.trees {
				if method == http.MethodOptions {
					continue
				}
				// Add request method to list of allowed methods
				allowed = append(allowed, method)
			}
		} else {
			return r.globalAllowed
		}
	} else { // specific path
		for method := range r.trees {
			// Skip the requested method - we already tried this one
			if method == reqMethod || method == http.MethodOptions {
				continue
			}

			handle, _, _ := r.trees[method].getValue(path, nil)
			if handle != nil {
				// Add request method to list of allowed methods
				allowed = append(allowed, method)
			}
		}
	}

	if len(allowed) > 0 {
		// Add request method to list of allowed methods
		allowed = append(allowed, http.MethodOptions)

		// Sort allowed methods.
		// sort.Strings(allowed) unfortunately causes unnecessary allocations
		// due to allowed being moved to the heap and interface conversion
		for i, l := 1, len(allowed); i < l; i++ {
			for j := i; j > 0 && allowed[j] < allowed[j-1]; j-- {
				allowed[j], allowed[j-1] = allowed[j-1], allowed[j]
			}
		}

		// return as comma separated list
		return strings.Join(allowed, ", ")
	}

	return allow
}

// ServeHTTP makes the router implement the http.Handler interface.
func (r *Router) ServeHTTP(wo http.ResponseWriter, req *http.Request) {

	// wrap the original response writer into a negroni like response writer and go
	// with that
	//
	// see: https://github.com/urfave/negroni/blob/master/response_writer.go
 	w := NewResponseWriter(wo)

	// if panics should be handled, "register" the handling
	if r.PanicHandler != nil {
		defer r.recv(w, req)
	}

	path := req.URL.Path

	// if there is paths registered for the method (incl. OPTIONS)
	if root := r.trees[req.Method]; root != nil {

		// try to match a registered handler
		handle, ps, tsr := root.getValue(path, r.getParams)

		// if there is a handler registered for this path (this is the "happy path")
		if handle != nil {

			// if parameters where extracted from the path
			if ps != nil {

				// acquire a context object
				c := AcquireContextObject()

				// wrap request, response and parameters in the context object
				c.Request = req
				c.Response = w
				c.Params = *ps

				// handle the request
				handle(c)

				// release the context object
				ReleaseContextObject(c)

				// release the parameters
				r.putParams(ps)

			} else {

				// acquire a context object
				c := AcquireContextObject()

				// wrap request and response in the context object
				c.Request = req
				c.Response = w

				// handle the request
				handle(c)
				// release the context object
				ReleaseContextObject(c)
			}

			// done serving the request
			//log.Info().Str("method", req.Method).Int("status", w.Status()).Msg(w.Error().Error())
			log.Info().Str("method", req.Method).Int("status", w.Status()).Msg("")
			return
		}

		// no matching path but try to fix the path (trailing slashes and case), unless
		// CONNECT or the root path
		if req.Method != http.MethodConnect && path != "/" {

			// set status 301 for GETs and 308 for all other methods
			code := http.StatusMovedPermanently
			if req.Method != http.MethodGet {
				code = http.StatusPermanentRedirect
			}

			// if there is a trailing slash recommendation and RedirectTrailingSlash is set,
			// redirect there
			if tsr && r.RedirectTrailingSlash {
				if len(path) > 1 && path[len(path)-1] == '/' {
					req.URL.Path = path[:len(path)-1]
				} else {
					req.URL.Path = path + "/"
				}

				// redirect to the tsr-fixed URL
				http.Redirect(w, req, req.URL.String(), code)

				log.Info().Str("method", req.Method).Int("status", code).Msg("")

				// done serving the request
				return
			}

			// if RedirectFixedPath is set, try to fix the and redirect case-errors
			if r.RedirectFixedPath {

				// do a case insensitive path lookup
				fixedPath, found := root.findCaseInsensitivePath(
					CleanPath(path),
					r.RedirectTrailingSlash,
				)

				// if a path could be found through case insensitive lookup, redirect to the
				// correct path
				if found {
					req.URL.Path = fixedPath

					// redirect to the case-fixed URL
					http.Redirect(w, req, req.URL.String(), code)

					// done serving the request
					return
				}
			}
		}
	}

	// if it is an OPTIONS request (which was not handled above) and HandleOptions is true
	if req.Method == http.MethodOptions && r.HandleOptions {

			// if there is any method allowed on this path
			if allow := r.allowed(path, http.MethodOptions); allow != "" {

				// if there is OPTIONS callback function
				if r.Options != nil {

					// call the function (feeding the list of allowed method)
					r.Options(w, req, allow)

				} else {

					// call the default function (feeding the list of allowed method)
					defaultOptions(w, req, allow)
				}

				// done serving the request
				return
			}

		// if we get here, the OPTIONS request falls through to not found

	} else {

		// if HandleMethodNotAllowed (405) is true
		if r.HandleMethodNotAllowed {

			// if there methods allowed on the requested path
			if allow := r.allowed(path, req.Method); allow != "" {

				// if there is Method not allowed callback function
				if r.MethodNotAllowed != nil {

					// call the function (feeding the list of allowed method)
					r.MethodNotAllowed(w, req, allow)
				} else {

					// call the default function (feeding the list of allowed method)
					defaultMethodNotAllowed(w, req, allow)
				}

				// done serving the request
				return
			}
		}

		// if we get here, any no OPTIONS request falls through to not found
	}

	// Not found, respond with a custom callback or the default one.
	if r.NotFound != nil {

		// call the custom callback
		r.NotFound(w, req)
	} else {

		// call the default callback
		defaultNotFound(w, req)
	}
}

func defaultNotFound(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(HeaderContentType, MIMEApplicationJSONCharsetUTF8)
	w.WriteHeader(http.StatusNotFound)
	bytes, _ := json.Marshal(struct {
		Error string `json:"error"`
	}{Error: http.StatusText(http.StatusNotFound)})
	w.Write(bytes)
}

func defaultMethodNotAllowed(w http.ResponseWriter, r *http.Request, allow string) {

	// see: https://tools.ietf.org/html/rfc7231#section-6.5.5

	w.Header().Set("Allow", allow)
	w.Header().Set(HeaderContentType, MIMEApplicationJSONCharsetUTF8)
	w.WriteHeader(http.StatusMethodNotAllowed)
	bytes, _ := json.Marshal(struct {
		Error string `json:"error"`
	}{Error: http.StatusText(http.StatusMethodNotAllowed)})
	w.Write(bytes)
}

func defaultOptions(w http.ResponseWriter, r *http.Request, allow string) {

	// if the Access-Control-Request-Method header is set
	//
	// if the Access-Control-Request-Method header is not set, we simply return 204
	// which will handled by the browser as unsuccessful preflight
	if r.Header.Get(HeaderAccessControlRequestMethod) != "" {

		// set header indicating all methods for this path
		w.Header().Set("Access-Control-Allow-Methods", allow)

		// set header indicating ALL(!) origins
		w.Header().Set("Access-Control-Allow-Origin", "*")
	}

	// set status code to 204
	w.WriteHeader(http.StatusNoContent)
	w.Write(nil)
}


// MIME types
const (
	MIMEApplicationJSON                  = "application/json"
	MIMEApplicationJSONCharsetUTF8       = MIMEApplicationJSON + "; " + charsetUTF8
	MIMEApplicationJavaScript            = "application/javascript"
	MIMEApplicationJavaScriptCharsetUTF8 = MIMEApplicationJavaScript + "; " + charsetUTF8
	MIMEApplicationXML                   = "application/xml"
	MIMEApplicationXMLCharsetUTF8        = MIMEApplicationXML + "; " + charsetUTF8
	MIMETextXML                          = "text/xml"
	MIMETextXMLCharsetUTF8               = MIMETextXML + "; " + charsetUTF8
	MIMEApplicationForm                  = "application/x-www-form-urlencoded"
	MIMEApplicationProtobuf              = "application/protobuf"
	MIMEApplicationMsgpack               = "application/msgpack"
	MIMETextHTML                         = "text/html"
	MIMETextHTMLCharsetUTF8              = MIMETextHTML + "; " + charsetUTF8
	MIMETextPlain                        = "text/plain"
	MIMETextPlainCharsetUTF8             = MIMETextPlain + "; " + charsetUTF8
	MIMEMultipartForm                    = "multipart/form-data"
	MIMEOctetStream                      = "application/octet-stream"
)

const (
	charsetUTF8 = "charset=UTF-8"
	// PROPFIND Method can be used on collection and property resources.
	PROPFIND = "PROPFIND"
	// REPORT Method can be used to get information about a resource, see rfc 3253
	REPORT = "REPORT"
)

// Headers
const (
	HeaderAccept              = "Accept"
	HeaderAcceptEncoding      = "Accept-Encoding"
	HeaderAllow               = "Allow"
	HeaderAuthorization       = "Authorization"
	HeaderContentDisposition  = "Content-Disposition"
	HeaderContentEncoding     = "Content-Encoding"
	HeaderContentLength       = "Content-Length"
	HeaderContentType         = "Content-Type"
	HeaderCookie              = "Cookie"
	HeaderSetCookie           = "Set-Cookie"
	HeaderIfModifiedSince     = "If-Modified-Since"
	HeaderLastModified        = "Last-Modified"
	HeaderLocation            = "Location"
	HeaderUpgrade             = "Upgrade"
	HeaderVary                = "Vary"
	HeaderWWWAuthenticate     = "WWW-Authenticate"
	HeaderXForwardedFor       = "X-Forwarded-For"
	HeaderXForwardedProto     = "X-Forwarded-Proto"
	HeaderXForwardedProtocol  = "X-Forwarded-Protocol"
	HeaderXForwardedSsl       = "X-Forwarded-Ssl"
	HeaderXUrlScheme          = "X-Url-Scheme"
	HeaderXHTTPMethodOverride = "X-HTTP-Method-Override"
	HeaderXRealIP             = "X-Real-IP"
	HeaderXRequestID          = "X-Request-ID"
	HeaderXRequestedWith      = "X-Requested-With"
	HeaderServer              = "Server"
	HeaderOrigin              = "Origin"

	// Access control
	HeaderAccessControlRequestMethod    = "Access-Control-Request-Method"
	HeaderAccessControlRequestHeaders   = "Access-Control-Request-Headers"
	HeaderAccessControlAllowOrigin      = "Access-Control-Allow-Origin"
	HeaderAccessControlAllowMethods     = "Access-Control-Allow-Methods"
	HeaderAccessControlAllowHeaders     = "Access-Control-Allow-Headers"
	HeaderAccessControlAllowCredentials = "Access-Control-Allow-Credentials"
	HeaderAccessControlExposeHeaders    = "Access-Control-Expose-Headers"
	HeaderAccessControlMaxAge           = "Access-Control-Max-Age"

	// Security
	HeaderStrictTransportSecurity         = "Strict-Transport-Security"
	HeaderXContentTypeOptions             = "X-Content-Type-Options"
	HeaderXXSSProtection                  = "X-XSS-Protection"
	HeaderXFrameOptions                   = "X-Frame-Options"
	HeaderContentSecurityPolicy           = "Content-Security-Policy"
	HeaderContentSecurityPolicyReportOnly = "Content-Security-Policy-Report-Only"
	HeaderXCSRFToken                      = "X-CSRF-Token"
	HeaderReferrerPolicy                  = "Referrer-Policy"
)
