package main

import (
	"archive/zip"
	"bytes"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dgrijalva/jwt-go"
	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	"github.com/satori/go.uuid"

	"github.com/TykTechnologies/tyk/apidef"
	"github.com/TykTechnologies/tyk/config"
	"github.com/TykTechnologies/tyk/storage"
	"github.com/TykTechnologies/tyk/test"
	"github.com/TykTechnologies/tyk/user"
)

// map[bundleName]map[fileName]fileContent
var testBundles = map[string]map[string]string{}
var testBundleMu sync.Mutex

func registerBundle(name string, files map[string]string) string {
	testBundleMu.Lock()
	defer testBundleMu.Unlock()

	bundleID := name + "-" + uuid.NewV4().String() + ".zip"
	testBundles[bundleID] = files

	return bundleID
}

func bundleHandleFunc(w http.ResponseWriter, r *http.Request) {
	testBundleMu.Lock()
	defer testBundleMu.Unlock()

	bundleName := strings.Replace(r.URL.Path, "/bundles/", "", -1)
	bundle, exists := testBundles[bundleName]
	if !exists {
		log.Warning(testBundles)
		http.Error(w, "Bundle not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/zip")

	z := zip.NewWriter(w)
	for name, content := range bundle {
		f, _ := z.Create(name)
		f.Write([]byte(content))
	}
	z.Close()
}

type testHttpResponse struct {
	Method  string
	URI     string
	Url     string
	Body    string
	Headers map[string]string
	Form    map[string]string
}

const (
	// We need a static port so that the urls can be used in static
	// test data, and to prevent the requests from being randomized
	// for checksums. Port 16500 should be obscure and unused.
	testHttpListen = "127.0.0.1:16500"
	// Accepts any http requests on /, only allows GET on /get, etc.
	// All return a JSON with request info.
	testHttpAny     = "http://" + testHttpListen
	testHttpGet     = testHttpAny + "/get"
	testHttpPost    = testHttpAny + "/post"
	testHttpJWK     = testHttpAny + "/jwk.json"
	testHttpBundles = testHttpAny + "/bundles/"

	// Nothing should be listening on port 16501 - useful for
	// testing TCP and HTTP failures.
	testHttpFailure    = "127.0.0.1:16501"
	testHttpFailureAny = "http://" + testHttpFailure
)

func testHttpHandler() *mux.Router {
	var upgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
	}

	wsHandler := func(w http.ResponseWriter, req *http.Request) {
		conn, err := upgrader.Upgrade(w, req, nil)
		if err != nil {
			http.Error(w, fmt.Sprintf("cannot upgrade: %v", err), http.StatusInternalServerError)
		}

		// start simple reader/writer per connection
		go func() {
			for {
				mt, p, err := conn.ReadMessage()
				if err != nil {
					return
				}
				conn.WriteMessage(mt, []byte("reply to message: "+string(p)))
			}
		}()
	}

	httpError := func(w http.ResponseWriter, status int) {
		http.Error(w, http.StatusText(status), status)
	}
	writeDetails := func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			httpError(w, http.StatusInternalServerError)
			return
		}
		r.URL.Opaque = r.URL.RawPath
		w.Header().Set("X-Tyk-Mock", "1")
		body, _ := ioutil.ReadAll(r.Body)

		err := json.NewEncoder(w).Encode(testHttpResponse{
			Method:  r.Method,
			URI:     r.RequestURI,
			Url:     r.URL.String(),
			Headers: firstVals(r.Header),
			Form:    firstVals(r.Form),
			Body:    string(body),
		})
		if err != nil {
			httpError(w, http.StatusInternalServerError)
		}
	}
	handleMethod := func(method string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if method != "" && r.Method != method {
				httpError(w, http.StatusMethodNotAllowed)
			} else {
				writeDetails(w, r)
			}
		}
	}

	// use gorilla's mux as it allows to cancel URI cleaning
	// (it is not configurable in standard http mux)
	r := mux.NewRouter()
	r.HandleFunc("/get", handleMethod("GET"))
	r.HandleFunc("/post", handleMethod("POST"))
	r.HandleFunc("/ws", wsHandler)
	r.HandleFunc("/jwk.json", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, jwkTestJson)
	})
	r.HandleFunc("/bundles/{rest:.*}", bundleHandleFunc)
	r.HandleFunc("/{rest:.*}", handleMethod(""))

	return r
}

const jwkTestJson = `{
    "keys": [{
        "alg": "RS256",
        "kty": "RSA",
        "use": "sig",
        "x5c": ["Ci0tLS0tQkVHSU4gUFVCTElDIEtFWS0tLS0tCk1JSUJJakFOQmdrcWhraUc5dzBCQVFFRkFBT0NBUThBTUlJQkNnS0NBUUVBeXFaNHJ3S0Y4cUNFeFM3a3BZNGMKbkphLzM3Rk1rSk5rYWxaM091c2xMQjBvUkw4VDRjOTRrZEY0YWVOelNGa1NlMm45OUlCSTZTc2w3OXZiZk1aYgordDA2TDBROTRrKy9QMzd4NysvUkpaaWZmNHkxVkdqcm5ybk1JMml1OWw0aUJCUll6Tm1HNmVibHJvRU1NV2xnCms1dHlzSGd4QjU5Q1NOSWNEOWdxazFoeDRuL0ZnT212S3NmUWdXSE5sUFNEVFJjV0dXR2hCMi9YZ05WWUcycE8KbFF4QVBxTGhCSGVxR1RYQmJQZkdGOWNIeml4cHNQcjZHdGJ6UHdoc1EvOGJQeG9KN2hkZm4rcnp6dGtzM2Q2KwpIV1VSY3lOVExSZTBtalhqamVlOVo2K2daK0grZlM0cG5QOXRxVDdJZ1U2ZVBVV1Rwam9pUHRMZXhnc0FhL2N0CmpRSURBUUFCCi0tLS0tRU5EIFBVQkxJQyBLRVktLS0tLQo="],
        "n": "xofiG8gsnv9-I_g-5OWTLhaZtgAGq1QEsBCPK9lmLqhuonHe8lT-nK1DM49f6J9QgaOjZ3DB50QkhBysnIFNcXFyzaYIPMoccvuHLPgdBawX4WYKm5gficD0WB0XnTt4sqTI5usFpuop9vvW44BwVGhRqMT7c11gA8TSWMBxDI4A5ARc4MuQtfm64oN-JQodSztArwb9wcmH8WrBvSUkR4pyi9MT8W27gqJ2e2Xn8jgGnswNQWOyCTN84PawOYaN-2ORHeIea1g-URln1bofcHN73vZCIrVbE6iA2D7Ybh22AVrCfunekEDEe2GZfLZLejiZiBWG7enJhcrQIzAQGw",
        "e": "AQAB",
        "kid": "12345",
        "x5t": "12345"
    }]
}`

func withAuth(r *http.Request) *http.Request {
	// This is the default config secret
	r.Header.Set("x-tyk-authorization", config.Global().Secret)
	return r
}

// TODO: replace with /tyk/keys/create call
func createSession(sGen ...func(s *user.SessionState)) string {
	key := generateToken("", "")
	session := createStandardSession()
	if len(sGen) > 0 {
		sGen[0](session)
	}
	if session.Certificate != "" {
		key = generateToken("", session.Certificate)
	}

	FallbackKeySesionManager.UpdateSession(storage.HashKey(key), session, 60, config.Global().HashKeys)
	return key
}

func createStandardPolicy() *user.Policy {
	return &user.Policy{
		Rate:             1000.0,
		Per:              1.0,
		QuotaMax:         -1,
		QuotaRenewalRate: -1,
		AccessRights:     map[string]user.AccessDefinition{},
		Active:           true,
		KeyExpiresIn:     60,
	}
}

func createPolicy(pGen ...func(p *user.Policy)) string {
	pID := keyGen.GenerateAuthKey("")
	pol := createStandardPolicy()
	pol.ID = pID

	if len(pGen) > 0 {
		pGen[0](pol)
	}

	policiesMu.Lock()
	policiesByID[pID] = *pol
	policiesMu.Unlock()

	return pID
}

func createJWKToken(jGen ...func(*jwt.Token)) string {
	// Create the token
	token := jwt.New(jwt.GetSigningMethod("RS512"))
	// Set the token ID

	if len(jGen) > 0 {
		jGen[0](token)
	}

	// Sign and get the complete encoded token as a string
	signKey, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(jwtRSAPrivKey))
	if err != nil {
		panic("Couldn't extract private key: " + err.Error())
	}
	tokenString, err := token.SignedString(signKey)
	if err != nil {
		panic("Couldn't create JWT token: " + err.Error())
	}

	return tokenString
}

func createJWKTokenHMAC(jGen ...func(*jwt.Token)) string {
	// Create the token
	token := jwt.New(jwt.SigningMethodHS256)
	// Set the token ID

	if len(jGen) > 0 {
		jGen[0](token)
	}

	tokenString, err := token.SignedString([]byte(jwtSecret))
	if err != nil {
		panic("Couldn't create JWT token: " + err.Error())
	}

	return tokenString
}

func firstVals(vals map[string][]string) map[string]string {
	m := make(map[string]string, len(vals))
	for k, vs := range vals {
		m[k] = vs[0]
	}
	return m
}

type tykTestServerConfig struct {
	sepatateControlAPI bool
	delay              time.Duration
	hotReload          bool
	overrideDefaults   bool
	coprocessConfig    config.CoProcessConfig
}

type tykTestServer struct {
	ln  net.Listener
	cln net.Listener
	URL string

	globalConfig config.Config
	config       tykTestServerConfig
}

func (s *tykTestServer) Start() {
	s.ln, _ = generateListener(0)
	_, port, _ := net.SplitHostPort(s.ln.Addr().String())
	globalConf := config.Global()
	globalConf.ListenPort, _ = strconv.Atoi(port)

	if s.config.sepatateControlAPI {
		s.cln, _ = net.Listen("tcp", "127.0.0.1:0")

		_, port, _ = net.SplitHostPort(s.cln.Addr().String())
		globalConf.ControlAPIPort, _ = strconv.Atoi(port)
	}

	globalConf.CoProcessOptions = s.config.coprocessConfig

	config.SetGlobal(globalConf)

	setupGlobals()
	// This is emulate calling start()
	// But this lines is the only thing needed for this tests
	if config.Global().ControlAPIPort == 0 {
		loadAPIEndpoints(mainRouter)
	}
	// Set up a default org manager so we can traverse non-live paths
	if !config.Global().SupressDefaultOrgStore {
		DefaultOrgStore.Init(getGlobalStorageHandler("orgkey.", false))
		DefaultQuotaStore.Init(getGlobalStorageHandler("orgkey.", false))
	}

	if s.config.hotReload {
		listen(s.ln, s.cln, nil)
	} else {
		listen(s.ln, s.cln, fmt.Errorf("Without goagain"))
	}

	s.URL = "http://" + s.ln.Addr().String()
	s.globalConfig = globalConf
}

func (s *tykTestServer) Close() {
	s.ln.Close()

	if s.config.sepatateControlAPI {
		s.cln.Close()
		globalConf := config.Global()
		globalConf.ControlAPIPort = 0
		config.SetGlobal(globalConf)
	}
}

func (s *tykTestServer) Do(tc test.TestCase) (*http.Response, error) {
	scheme := "http://"
	if s.globalConfig.HttpServerOptions.UseSSL {
		scheme = "https://"
	}

	if tc.Domain == "" {
		tc.Domain = "127.0.0.1"
	}

	baseUrl := scheme + strings.Replace(s.ln.Addr().String(), "[::]", tc.Domain, 1)
	baseUrl = strings.Replace(baseUrl, "127.0.0.1", tc.Domain, 1)

	if tc.ControlRequest {
		if s.config.sepatateControlAPI {
			baseUrl = scheme + s.cln.Addr().String()
		} else if s.globalConfig.ControlAPIHostname != "" {
			baseUrl = strings.Replace(baseUrl, "127.0.0.1", s.globalConfig.ControlAPIHostname, 1)
		}
	}

	req := test.NewRequest(tc)
	req.URL, _ = url.Parse(baseUrl + tc.Path)

	if tc.AdminAuth {
		req = withAuth(req)
	}

	if tc.Client == nil {
		tc.Client = &http.Client{
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}

	return tc.Client.Do(req)
}

func (s *tykTestServer) Run(t testing.TB, testCases ...test.TestCase) (*http.Response, error) {
	var lastResponse *http.Response
	var lastError error

	for ti, tc := range testCases {
		lastResponse, lastError = s.Do(tc)
		tcJSON, _ := json.Marshal(tc)

		if lastError != nil {
			if tc.ErrorMatch != "" {
				if !strings.Contains(lastError.Error(), tc.ErrorMatch) {
					t.Errorf("[%d] Expect error `%s` to contain `%s`. %s", ti, lastError.Error(), tc.ErrorMatch, string(tcJSON))
				}
			} else {
				t.Errorf("[%d] Connection error: %s. %s", ti, lastError.Error(), string(tcJSON))
			}
			continue
		} else if tc.ErrorMatch != "" {
			t.Error("Expect error.", string(tcJSON))
			continue
		}

		respCopy := copyResponse(lastResponse)
		if lastError = test.AssertResponse(respCopy, tc); lastError != nil {
			t.Errorf("[%d] %s. %s\n", ti, lastError.Error(), string(tcJSON))
		}

		delay := tc.Delay
		if delay == 0 {
			delay = s.config.delay
		}

		if delay > 0 {
			time.Sleep(delay)
		}
	}

	return lastResponse, lastError
}

func (s *tykTestServer) RunExt(t *testing.T, testCases ...test.TestCase) {
	var testMatrix = []struct {
		goagain          bool
		overrideDefaults bool
	}{
		{false, false},
		{false, true},
		{true, true},
		{true, false},
	}

	for i, m := range testMatrix {
		s.config.hotReload = m.goagain
		s.config.overrideDefaults = m.overrideDefaults

		if i > 0 {
			s.Close()
			s.Start()
		}

		title := fmt.Sprintf("hotReload: %v, overrideDefaults: %v", m.goagain, m.overrideDefaults)
		t.Run(title, func(t *testing.T) {
			s.Run(t, testCases...)
		})
	}
}

func (s *tykTestServer) createSession(sGen ...func(s *user.SessionState)) string {
	session := createStandardSession()
	if len(sGen) > 0 {
		sGen[0](session)
	}

	resp, err := s.Do(test.TestCase{
		Method: "POST",
		Path:   "/tyk/keys/create",
		Data:   session,
	})

	if err != nil {
		log.Fatal("Error while creating session:", err)
		return ""
	}

	respJSON := apiModifyKeySuccess{}
	err = json.NewDecoder(resp.Body).Decode(&respJSON)
	if err != nil {
		log.Fatal("Error while serializing session:", err)
		return ""
	}
	resp.Body.Close()

	return respJSON.Key
}

func newTykTestServer(config ...tykTestServerConfig) tykTestServer {
	s := tykTestServer{}
	if len(config) > 0 {
		s.config = config[0]
	}
	s.Start()

	return s
}

const sampleAPI = `{
    "api_id": "test",
    "use_keyless": true,
    "definition": {
        "location": "header",
        "key": "version"
    },
    "auth": {
        "auth_header_name": "authorization"
	},
    "version_data": {
        "not_versioned": true,
        "versions": {
            "v1": {
            	"name": "v1",
            	"use_extended_paths": true
           	}
        }
    },
    "proxy": {
        "listen_path": "/sample",
        "target_url": "` + testHttpAny + `"
    }
}`

func updateAPIVersion(spec *APISpec, name string, verGen func(version *apidef.VersionInfo)) {
	version := spec.VersionData.Versions[name]
	verGen(&version)
	spec.VersionData.Versions[name] = version
}

func jsonMarshalString(i interface{}) (out string) {
	b, _ := json.Marshal(i)
	return string(b)
}

func buildAPI(apiGens ...func(spec *APISpec)) (specs []*APISpec) {
	if len(apiGens) == 0 {
		apiGens = append(apiGens, func(spec *APISpec) {})
	}

	for _, gen := range apiGens {
		spec := &APISpec{APIDefinition: &apidef.APIDefinition{}}
		if err := json.Unmarshal([]byte(sampleAPI), spec.APIDefinition); err != nil {
			panic(err)
		}

		specs = append(specs, spec)
		gen(spec)
	}

	return specs
}

func loadAPI(specs ...*APISpec) (out []*APISpec) {
	globalConf := config.Global()
	oldPath := globalConf.AppPath
	globalConf.AppPath, _ = ioutil.TempDir("", "apps")
	config.SetGlobal(globalConf)

	defer func() {
		globalConf := config.Global()
		os.RemoveAll(globalConf.AppPath)
		globalConf.AppPath = oldPath
		config.SetGlobal(globalConf)
	}()

	for i, spec := range specs {
		specBytes, _ := json.Marshal(spec)
		specFilePath := filepath.Join(config.Global().AppPath, spec.APIID+strconv.Itoa(i)+".json")
		if err := ioutil.WriteFile(specFilePath, specBytes, 0644); err != nil {
			panic(err)
		}
	}

	doReload()

	for _, spec := range specs {
		out = append(out, getApiSpec(spec.APIID))
	}

	return out
}

func buildAndLoadAPI(apiGens ...func(spec *APISpec)) (specs []*APISpec) {
	return loadAPI(buildAPI(apiGens...)...)
}

// Taken from https://medium.com/@mlowicki/http-s-proxy-in-golang-in-less-than-100-lines-of-code-6a51c2f2c38c
type httpProxyHandler struct {
	proto    string
	URL      string
	server   *http.Server
	listener net.Listener
}

func (p *httpProxyHandler) handleTunneling(w http.ResponseWriter, r *http.Request) {
	dest_conn, err := net.DialTimeout("tcp", r.Host, 10*time.Second)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "Hijacking not supported", http.StatusInternalServerError)
		return
	}
	client_conn, _, err := hijacker.Hijack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
	}
	go p.transfer(dest_conn, client_conn)
	go p.transfer(client_conn, dest_conn)
}

func (p *httpProxyHandler) transfer(destination io.WriteCloser, source io.ReadCloser) {
	defer destination.Close()
	defer source.Close()
	io.Copy(destination, source)
}
func (p *httpProxyHandler) handleHTTP(w http.ResponseWriter, req *http.Request) {
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	defer resp.Body.Close()
	p.copyHeader(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func (p *httpProxyHandler) Stop() error {
	return p.server.Close()
}

func (p *httpProxyHandler) copyHeader(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

func initProxy(proto string, tlsConfig *tls.Config) *httpProxyHandler {
	proxy := &httpProxyHandler{proto: proto}

	proxy.server = &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodConnect {
				proxy.handleTunneling(w, r)
			} else {
				proxy.handleHTTP(w, r)
			}
		}),
		// Disable HTTP/2.
		TLSNextProto: make(map[string]func(*http.Server, *tls.Conn, http.Handler)),
	}

	var err error

	switch proto {
	case "http":
		proxy.listener, err = net.Listen("tcp", ":0")
	case "https":
		proxy.listener, err = tls.Listen("tcp", ":0", tlsConfig)
	default:
		log.Fatal("Unsupported proto scheme", proto)
	}

	if err != nil {
		log.Fatal(err)
	}

	proxy.URL = proto + "://" + proxy.listener.Addr().String()

	go proxy.server.Serve(proxy.listener)

	return proxy
}

func generateTestBinaryData() (buf *bytes.Buffer) {
	buf = new(bytes.Buffer)
	type testData struct {
		a float32
		b float64
		c uint32
	}
	for i := 0; i < 10; i++ {
		s := &testData{rand.Float32(), rand.Float64(), rand.Uint32()}
		binary.Write(buf, binary.BigEndian, s)
	}
	return buf
}
