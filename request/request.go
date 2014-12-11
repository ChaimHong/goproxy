// Wrapper around http.Request with additional features
package request

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/marbemac/stoplight/core/models"
	"github.com/marbemac/stoplight/core/netutils"
	"github.com/marbemac/stoplight/core/routers"

	"github.com/hashicorp/golang-lru"
	"github.com/jinzhu/gorm"
)

var (
	cache, _     = lru.New(128)
	dbConnection *gorm.DB
)

// Request is a rapper around http request that provides more info about http.Request
type Request interface {
	GetHttpRequest() *http.Request              // Original http request
	SetHttpRequest(*http.Request)               // Can be used to set http request
	GetId() int64                               // Request id that is unique to this running process
	SetBody(io.ReadCloser)                      // Sets request body
	GetBody() io.ReadCloser                     // Request body fully read and stored in effective manner (buffered to disk for large requests)
	AddAttempt(Attempt)                         // Add last proxy attempt to the request
	GetAttempts() []Attempt                     // Returns last attempts to proxy request, may be nil if there are no attempts
	GetLastAttempt() Attempt                    // Convenience method returning the last attempt, may be nil if there are no attempts
	String() string                             // Debugging string representation of the request
	SetUserData(key string, baton interface{})  // Provide storage space for data that survives with the request
	GetUserData(key string) (interface{}, bool) // Fetch user data set from previously SetUserData call
	DeleteUserData(key string)                  // Clean up user data set from previously SetUserData call
	SetDb() *gorm.DB                            // Set the DB for use in this request
	GetOrigin() *url.URL                        // The origin url (scheme + host + port), taking into account headers and environment
	GetProject() *models.Project                // The project id associated with this request
	GetEnvironment() *models.Environment        // The environment associated with this request
}

type Attempt interface {
	GetError() error
	GetDuration() time.Duration
	GetResponse() *http.Response
	// GetEndpoint() endpoint.Endpoint
}

type BaseAttempt struct {
	Error    error
	Duration time.Duration
	Response *http.Response
	// Endpoint endpoint.Endpoint
}

func (ba *BaseAttempt) GetResponse() *http.Response {
	return ba.Response
}

func (ba *BaseAttempt) GetError() error {
	return ba.Error
}

func (ba *BaseAttempt) GetDuration() time.Duration {
	return ba.Duration
}

// func (ba *BaseAttempt) GetEndpoint() endpoint.Endpoint {
//   return ba.Endpoint
// }

type BaseRequest struct {
	HttpRequest   *http.Request
	ReqHeaders    http.Header
	Id            int64
	Body          []byte
	Attempts      []Attempt
	Skip          bool
	env           *models.Environment
	project       *models.Project
	user          *models.User
	userDataMutex *sync.RWMutex
	userData      map[string]interface{}
	dbConnection  *gorm.DB
}

func NewBaseRequest(r *http.Request, id int64) *BaseRequest {
	var header = make(http.Header)
	netutils.CopyHeaders(header, r.Header)

	br := &BaseRequest{
		HttpRequest:   r,
		ReqHeaders:    header,
		Id:            id,
		Skip:          false,
		userDataMutex: &sync.RWMutex{},
	}
	br.SetBody(br.HttpRequest.Body)

	return br
}

func (br *BaseRequest) String() string {
	return fmt.Sprintf("Request(id=%d, method=%s, url=%s, attempts=%d)", br.Id, br.HttpRequest.Method, br.HttpRequest.URL.String(), len(br.Attempts))
}

func (br *BaseRequest) GetHttpRequest() *http.Request {
	return br.HttpRequest
}

func (br *BaseRequest) SetHttpRequest(r *http.Request) {
	br.HttpRequest = r
}

func (br *BaseRequest) GetId() int64 {
	return br.Id
}

func (br *BaseRequest) SetBody(b io.ReadCloser) {
	// Fetch the request body
	reqBody, err := ioutil.ReadAll(b)
	if err != nil {
		log.Println(err)
	}
	br.Body = reqBody

	// Copy it back to the response body to return to the client
	br.HttpRequest.Body = ioutil.NopCloser(bytes.NewBuffer(reqBody))
}

func (br *BaseRequest) GetBody() []byte {
	return br.Body
}

func (br *BaseRequest) AddAttempt(a Attempt) {
	br.Attempts = append(br.Attempts, a)
}

func (br *BaseRequest) GetAttempts() []Attempt {
	return br.Attempts
}

func (br *BaseRequest) GetLastAttempt() Attempt {
	if len(br.Attempts) == 0 {
		return nil
	}
	return br.Attempts[len(br.Attempts)-1]
}
func (br *BaseRequest) SetUserData(key string, baton interface{}) {
	br.userDataMutex.Lock()
	defer br.userDataMutex.Unlock()
	if br.userData == nil {
		br.userData = make(map[string]interface{})
	}
	br.userData[key] = baton
}
func (br *BaseRequest) GetUserData(key string) (i interface{}, b bool) {
	br.userDataMutex.RLock()
	defer br.userDataMutex.RUnlock()
	if br.userData == nil {
		return i, false
	}
	i, b = br.userData[key]
	return i, b
}
func (br *BaseRequest) DeleteUserData(key string) {
	br.userDataMutex.Lock()
	defer br.userDataMutex.Unlock()
	if br.userData == nil {
		return
	}

	delete(br.userData, key)
}

func (br *BaseRequest) SetDb(db *gorm.DB) {
	br.dbConnection = db
}

func (br *BaseRequest) GetOrigin() (u *url.URL) {
	// First check the header
	targetUrl := br.ReqHeaders.Get("X-StopLight-Url-Host")
	if targetUrl == "" {
		// else check the environment
		env := br.GetEnvironment()
		if env.Slug == "" {
			u = br.HttpRequest.URL
			return u
		}

		if env.Ssl {
			targetUrl = "https://" + env.Url
		} else {
			targetUrl = "http://" + env.Url
		}
	}

	u, _ = url.Parse(targetUrl)

	return
}

// Fetch and set the current user for the request
func (br *BaseRequest) GetUser() *models.User {
	if br.user == nil {
		header := br.ReqHeaders.Get("X-StopLight-Authorization")
		br.user = routers.CurrentUser(header, br.dbConnection)
	}

	return br.user
}

// Get the project first from the StopLight-Project header
// second from the environment if found.
func (br *BaseRequest) GetProject() *models.Project {
	if br.project != nil {
		return br.project
	}

	identifier := br.ReqHeaders.Get("X-StopLight-Project")
	var project models.Project

	if identifier == "" {
		env := br.GetEnvironment()
		identifier = env.ProjectId
	}

	if identifier != "" {
		existing, ok := cache.Get(identifier)
		if ok == true {
			project = existing.(models.Project)
		} else {
			result := br.dbConnection.Where("id = ?", identifier, true).First(&project)

			cache.Add(identifier, project)
			if result.Error != nil {
				// TODO: Inform the user somehow..
				// log.Println("Could not find project.")
			}
		}
	}

	return &project
}

func (br *BaseRequest) GetEnvironment() *models.Environment {
	if br.env != nil {
		return br.env
	}

	var env models.Environment
	env = br.requestEnvFromPath()
	if env.Id == "" {
		env = br.requestEnvFromHost()
	}

	return &env
}

/////////////
// HELPERS //
/////////////

// NOTE: Disabled the cache in the two functions below because when the user changes
// the environment state from running -> not running, cache is not busted.

func (br *BaseRequest) requestEnvFromPath() (env models.Environment) {
	identifier := slugFromUrl(br.HttpRequest.URL.RequestURI())
	// existing, ok := cache.Get(identifier)
	// if ok == true {
	// 	env = existing.(models.Environment)
	// } else {
	result := br.dbConnection.Where("slug = ? AND running = ?", identifier, true).First(&env)

	// cache.Add(identifier, env)
	if result.Error != nil {
		// TODO: Inform the user somehow..
		// log.Println("Could not find environment.")
		return
	}
	// }
	return
}

func (br *BaseRequest) requestEnvFromHost() (env models.Environment) {
	identifier := br.HttpRequest.URL.Host
	// existing, ok := cache.Get(identifier)
	// if ok == true {
	// 	env = existing.(models.Environment)
	// } else {
	result := br.dbConnection.Where("url = ? AND running = ?", identifier, true).First(&env)

	// cache.Add(identifier, env)
	if result.Error != nil {
		// TODO: Inform the user somehow..
		// log.Println("Could not find environment.")
		return
	}
	// }
	return
}

// Given a URL return the environment identifier
func slugFromUrl(url string) (identifier string) {
	parts := strings.Split(url, "/")
	if string(url[0]) == "/" {
		identifier = parts[1]
	} else {
		identifier = parts[0]
	}

	return
}
