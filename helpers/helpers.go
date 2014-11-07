package helpers

import (
	"bytes"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/marbemac/goproxy"
	"github.com/marbemac/goproxy/request"

	"github.com/marbemac/stoplight/db"
	"github.com/marbemac/stoplight/models"
	"github.com/marbemac/stoplight/sockets"

	"github.com/jinzhu/gorm"
)

const allowedCorsMethods = "GET,POST,PUT,PATCH,DELETE,COPY,HEAD,OPTIONS,LINK,UNLINK,PURGE,LOCK,UNLOCK,PROPFIND"

var (
	requestData  = make(map[int64]*request.BaseRequest)
	dbConnection *gorm.DB
	p            *goproxy.ProxyHttpServer
)

func InitProxyDB(dbType string, verbose bool) {
	dbConnection = db.New(dbType, verbose)
}

///////////////////
// PROXY FILTERS //
///////////////////

// Inits and sets the request object for later use
func SetupRequest(r *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
	baseReq := request.NewBaseRequest(r, ctx.Session)
	baseReq.SetDb(dbConnection)

	requestData[ctx.Session] = baseReq

	return r, nil
}

func PreflightCorsSupport(r *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
	// If it's an options request, return right away
	if r.Method == "OPTIONS" {
		resp := goproxy.NewResponse(r, goproxy.ContentTypeText, http.StatusOK, "")
		resp.Header.Set("Access-Control-Allow-Credentials", "true")
		resp.Header.Set("Access-Control-Allow-Headers", r.Header.Get("Access-Control-Request-Headers"))
		resp.Header.Set("Access-Control-Allow-Methods", allowedCorsMethods)
		resp.Header.Set("Access-Control-Allow-Origin", "*")
		resp.Header.Set("Access-Control-Expose-Headers", "Content-Length")

		requestData[ctx.Session].Skip = true

		return r, resp
	} else if requestData[ctx.Session].GetEnvironment().Slug == "" { // no environment? just pass it on
		return r, nil
	} else { // set request headers
		r.Header.Set("Access-Control-Allow-Credentials", "true")
		r.Header.Set("Access-Control-Allow-Headers", r.Header.Get("Access-Control-Request-Headers"))
		r.Header.Set("Access-Control-Allow-Methods", allowedCorsMethods)
		r.Header.Set("Access-Control-Allow-Origin", "*")
		r.Header.Set("Access-Control-Expose-Headers", "Content-Length")

		return r, nil
	}
}

func CleanRequest(r *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
	// Skip if no environment found
	env := requestData[ctx.Session].GetEnvironment()
	if env.Slug == "" {
		return r, nil
	}

	var targetUrl string
	if env.Ssl {
		targetUrl = "https://" + env.Url
	} else {
		targetUrl = "http://" + env.Url
	}

	url, err := url.Parse(targetUrl)
	if err != nil {
	}

	r.RequestURI = ""
	r.URL.Scheme = url.Scheme
	r.URL.Host = url.Host
	r.Host = url.Host
	r.URL.Path = urlWithoutEnvironment(env, r.URL.Path)
	r.Header.Add("StopLight-Request", "true")
	r.Header.Add("Host", r.URL.Host)

	return r, nil
}

func PostflightCorsSupport(resp *http.Response, ctx *goproxy.ProxyCtx) *http.Response {
	// Skip if no environment found, or no response
	if requestData[ctx.Session].Skip || requestData[ctx.Session].GetEnvironment().Slug == "" || resp == nil {
		return resp
	}

	resp.Header.Set("Access-Control-Allow-Credentials", "true")
	resp.Header.Set("Access-Control-Allow-Headers", ctx.Req.Header.Get("Access-Control-Request-Headers"))
	resp.Header.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS, DELETE, PUT, PATCH")
	resp.Header.Set("Access-Control-Allow-Origin", "*")
	return resp
}

func SaveStopLightRequest(resp *http.Response, ctx *goproxy.ProxyCtx) *http.Response {
	// Get the current data, and then delete the saved request data
	baseRequest := requestData[ctx.Session]
	if baseRequest.Skip {
		return resp
	}
	defer delete(requestData, ctx.Session)

	valid := isValidResponse(baseRequest.HttpRequest, resp)
	env := baseRequest.GetEnvironment()
	if valid && env.Slug != "" {

		// If no response, create a dummy one
		if resp == nil {
			resp = goproxy.NewResponse(ctx.Req, goproxy.ContentTypeText, http.StatusServiceUnavailable, "Service not available. Is the server running?")
		}

		// Assign the response body, for later
		var respBody []byte
		var err error
		respBody, err = ioutil.ReadAll(resp.Body)
		if err != nil {
			log.Println(err)
		}
		// Copy it back to the response body to return to the client
		resp.Body = ioutil.NopCloser(bytes.NewBuffer(respBody))

		go func() {
			// save the request
			slrequest := models.NewRequest(env, baseRequest.HttpRequest, baseRequest.GetBody(), resp, respBody)
			result := dbConnection.Create(slrequest)
			if result.Error != nil {
				log.Println(result.Error)
				return
			}

			// send the data down to the client
			sockets.WebSocketHub.BroadcastEvent("request.create", "request", slrequest)
		}()
	}

	return resp
}

/////////////
// HELPERS //
/////////////

// Given a environment and URL, return the url with any environment info removed
func urlWithoutEnvironment(env *models.Environment, url string) (newUrl string) {
	newUrl = strings.Replace(url, env.Slug, "", 1)
	if len(newUrl) > 1 && string(newUrl[1]) == "/" {
		newUrl = strings.TrimPrefix(newUrl, "/")
	} else if len(newUrl) == 0 {
		newUrl = "/"
	}

	return
}

// Valid IF
// Is ajax request
// Is json/xml response
// Is not a get request
// No response or response body, but is not a get request
// Response is not 2xx series
// StopLight-Ignore header is not true
func isValidResponse(req *http.Request, resp *http.Response) (valid bool) {
	if req.Header.Get("StopLight-Ignore") == "true" {
		return
	}

	// nil response usually means 500
	if resp == nil {
		valid = true
	} else {
		isAjax := req.Header.Get("X-Requested-With")
		contentType := resp.Header.Get("Content-Type")
		isGet := req.Method == "GET"
		validStatusCode := resp.StatusCode == 304 || string(strconv.Itoa(resp.StatusCode)[0]) == "2"
		valid = !isGet || isAjax != "" || !validStatusCode || strings.Contains(contentType, "json") || strings.Contains(contentType, "xml")
	}

	return
}
