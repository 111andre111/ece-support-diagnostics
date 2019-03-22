package ecediag

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"html/template"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/elastic/beats/libbeat/logp"
	"golang.org/x/crypto/ssh/terminal"
)

var username string
var passwd string

// globally disable if used in main()
// http.DefaultTransport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true}

// Connection timeout = 5s
// TLS Handshake Timeout = 5s
var tr = &http.Transport{
	TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	Dial: (&net.Dialer{
		Timeout: 5 * time.Second,
	}).Dial,
	TLSHandshakeTimeout: 5 * time.Second,
}

// HTTP Timeout = 10s
var myClient = &http.Client{Timeout: 10 * time.Second, Transport: tr}

// https://itnext.io/how-to-stub-requests-to-remote-hosts-with-go-6c2c1db32bf2
// type Client struct {
// 	key, secret string
// 	httpClient  *http.Client
// }
//
// func NewClient(key, secret string, options ...Option) *Client {
// 	cli := Client{
// 		key:    key,
// 		secret: secret,
// 		httpClient: &http.Client{
// 			Timeout: 5 * time.Second,
// 		},
// 	}
//
// 	return &cli
// }

var cloudHost string

// RunRest starts the chain of functions to collect the Rest/HTTP calls
func RunRest(d types.Container, tar *Tarball) {
	var err error
	cloudHost, err = resolveCloudUI(d)
	panicError(err)

	fmt.Println("[ ] Collecting API information ECE and Elasticsearch")
	var wg sync.WaitGroup
	for _, item := range rest {
		wg.Add(1)
		go fetch(item, tar, &wg)

	}
	wg.Wait()

	clearStdoutLine()
	fmt.Println("[✔] Collected API information ECE and Elasticsearch")
}

// resolveCloudUI tries to determine the endpoint to talk to for the frc-cloud-uis-cloud-ui container
func resolveCloudUI(c types.Container) (endpoint string, err error) {
	log := logp.NewLogger("API")
	log.Infof("Running Resolver for Cloud UI")
	// Ports:[{IP:0.0.0.0 PrivatePort:5643 PublicPort:12443 Type:tcp} {IP:0.0.0.0 PrivatePort:5601 PublicPort:12400 Type:tcp}]

	var url string
	var failures []error

	for _, endpoint := range c.Ports {

		protocols := []string{"http", "https"}

		for _, protocol := range protocols {
			// {"ok":true,"message":"Love is the law, love under will, admin.","eula_accepted":true,"hrefs":{"regions":"http://0.0.0.0:12443/api/v0/regions","elasticsearch":"http://0.0.0.0:12443/api/v0/elasticsearch","logs":"http://0.0.0.0:12443/api/v0/logs","database/users":"http://0.0.0.0:12443/api/v0/database/users"}}
			// url = fmt.Sprintf("https://%s:%d/", endpoint.IP, endpoint.PublicPort)
			url = fmt.Sprintf("%s://%s:%d/", protocol, endpoint.IP, endpoint.PublicPort)

			// TODO: change to use: "api/v1/platform"
			req, err := http.NewRequest("GET", url+"api/v0", nil)
			if err != nil {
				failures = append(failures, err)
				continue
			}

			resp, err := myClient.Do(req)
			if err != nil {
				failures = append(failures, err)
				continue
			}
			if resp.StatusCode == 401 {
				// HTTP/1.1 401 Unauthorized
				err = ValidateAuth(req)
				if err != nil {
					return url, err
				}
				return url, nil
			}
			// non 200 OK
			err = fmt.Errorf("Response Code: %d, Body: %s", resp.StatusCode, resp.Body)
			failures = append(failures, err)
		} // End of protocols loop
	} // End of c.Ports loop
	err = fmt.Errorf("Could not find a working URL to talk to frc-cloud-uis-cloud-ui")
	return url, err
}

// ValidateAuth prompts and validates credentials to talk to frc-cloud-uis-cloud-ui container
func ValidateAuth(req *http.Request) error {
	log := logp.NewLogger("ValidateAuth")

	username, passwd = getCredentials()
	fmt.Println()

	req.SetBasicAuth(username, passwd)
	resp, err := myClient.Do(req)
	panicError(err)

	if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		defer resp.Body.Close()
		v0Response := new(v0APIresponse)
		json.NewDecoder(resp.Body).Decode(v0Response)
		// alog.Infof("%+v\n", v0Response)
		if v0Response.Ok {
			for i := 0; i <= 2; i++ {
				clearStdoutLine()
				// fmt.Printf("\033[F") // back to previous line
				// fmt.Printf("\033[K") // clear line
			}

			fmt.Printf("Authenticated\n")
			fmt.Printf("\t✔ Username (%s)\n", username)
			fmt.Printf("\t✔ Password\n")

			log.Infof("Cloud UI Resolved, using %s", req.URL)
			return nil
		}
	}
	return fmt.Errorf("Authentication failed")
}

// fetch dispatches the Rest/HTTP request
func fetch(it Rest, tar *Tarball, wg *sync.WaitGroup) {
	url := cloudHost + strings.TrimLeft(it.Request, "/")

	req, err := http.NewRequest("GET", url, nil)

	req.SetBasicAuth(username, passwd)
	req.Header.Set("X-Management-Request", "true")
	resp, err := myClient.Do(req)
	if err != nil {
		log.Fatal(url, err)
	}
	// if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
	//
	// }
	bodyText, err := ioutil.ReadAll(resp.Body)

	archiveFile := filepath.Join(cfg.DiagName, it.Filename)
	tar.AddData(archiveFile, bodyText)

	checkSubItems(it, bodyText, tar)

	wg.Done()
}

// checkSubItems is used when `Sub` is defined in the Rest object, and contains a `Loop` item.
//  It tries to unpack the parent JSON response into a map, and assert the proper type (array/object)
func checkSubItems(parent Rest, r []byte, tar *Tarball) {
	if len(parent.Sub) > 0 {

		var resp interface{}
		resp = readJSON(r)

		switch json := resp.(type) {

		case []interface{}:
			// Json response for the parent Rest response is a JSON Array
			fmt.Println(json, "Array!")
			iterateSub(parent, resp, tar)

		case map[string]interface{}:
			// Json response for parent Rest response is a JSON Object
			if parent.Loop == "" {
				// Iterate with top level map
				iterateSub(parent, resp, tar)
			} else {
				// Loop should specify a key that contains an Array
				d := json[parent.Loop]
				for _, Item := range d.([]interface{}) {
					iterateSub(parent, Item, tar)
				}
			}

		default:
			fmt.Println("SHIT!!!!")
		}
	}
}

func iterateSub(R Rest, It interface{}, tar *Tarball) {
	var wg sync.WaitGroup
	l := logp.NewLogger("Elasticsearch")

	s := It.(map[string]interface{})
	l.Infof("Gathering cluster diagnostic: %s, %s", s["cluster_id"], s["cluster_name"])

	for _, item := range R.Sub {
		wg.Add(1)

		// render template
		item.templater(It)

		go fetch(item, tar, &wg)
	}
	wg.Wait()
}

// templater when called runs templating for the defined fields
func (R *Rest) templater(Obj interface{}) {
	R.Filename = runTemplate(R.Filename, Obj)
	R.Request = runTemplate(R.Request, Obj)
}

// runTemplate performs the string substitution using the html/template package
func runTemplate(item string, Obj interface{}) string {

	t := template.Must(template.New("testing").Parse(item))

	var tpl bytes.Buffer

	err := t.Execute(&tpl, Obj)
	if err != nil {
		log.Println("executing template:", Obj)
	}
	return tpl.String()
}

// readJSON unpacks the Rest/HTTP request into a generic interface
func readJSON(in []byte) interface{} {
	var data interface{}
	err := json.Unmarshal(in, &data)
	panicError(err)
	return data
}

// getCredentials is used for securely prompting for a password from stdin
//  it uses the x/crypto/ssh/terminal package to ensure stdin echo is disabled
func getCredentials() (string, string) {
	fmt.Println("Please Enter Your ECE Admin Credentials")

	reader := bufio.NewReader(os.Stdin)

	fmt.Print("Enter Username: ")
	username, _ := reader.ReadString('\n')
	// fmt.Println("Username (read-only)")

	fmt.Print("Enter Password: ")
	bytePassword, err := terminal.ReadPassword(int(syscall.Stdin))
	if err == nil {
		// fmt.Println("")
		// fmt.Println("\nPassword typed: " + string(bytePassword))
	}
	password := string(bytePassword)

	return strings.TrimSpace(username), strings.TrimSpace(password)
	// return "readonly", strings.TrimSpace(password)
}
