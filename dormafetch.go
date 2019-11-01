package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/user"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh/terminal"

	"github.com/Azure/go-ntlmssp"
)

const (
	sessionCookieName = "ASP.NET_SessionId"
	urlDormaLogin     = "https://%s/scripts/login.aspx"
	urlDormaLogout    = "https://%s/scripts/login.aspx?sessiontimedout=2"
	urlDormaEntries   = "https://%s/scripts/buchungen/buchungsdata2.aspx?mode=0"

	// EntryTypeCome denotes an entry when entering the company.
	EntryTypeCome EntryType = "come"
	// EntryTypeLeave denotes an entry when leaving the company.
	EntryTypeLeave EntryType = "leave"
)

var (
	// ConfigDir denotes the directory to store host and credential information.
	ConfigDir string
)

func init() {
	usr, err := user.Current()
	if err == nil {
		ConfigDir = path.Join(usr.HomeDir, ".dorma")
	}
}

// GetDefaultDormaHost returns the default dorma host configured for the application or asks the user.
func GetDefaultDormaHost(appID string) (string, error) {
	//TODO handle empty config dir parameter

	hostsFile := path.Join(ConfigDir, "app-hosts")
	hosts, err := readAppHosts(hostsFile)
	if err != nil {
		return "", err
	}

	if host, ok := hosts[appID]; ok {
		return host, nil
	}

	fmt.Println(fmt.Sprintf("No Dorma host for app %q defined. Please enter host below:", appID))
	fmt.Print("> ")
	host, err := readString()
	if err != nil {
		return "", err
	}

	hosts[appID] = host
	writeAppHosts(hostsFile, hosts)

	return host, nil
}

func readAppHosts(file string) (map[string]string, error) {
	data, err := ioutil.ReadFile(file)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]string), nil
		}
		return nil, err
	}

	var hosts map[string]string
	if err := json.Unmarshal(data, &hosts); err != nil {
		return nil, err
	}

	return hosts, nil
}

func writeAppHosts(file string, hosts map[string]string) error {
	if err := os.MkdirAll(path.Dir(file), os.ModePerm); err != nil {
		return err
	}

	data, err := json.Marshal(&hosts)
	if err != nil {
		return err
	}

	return ioutil.WriteFile(file, data, os.ModePerm)
}

type credential struct {
	User string `json:"user"`
	Pass string `json:"pass"`
}

// GetCredentials returns the user credentials for a given Dorma Host by using a pre-configured environment or asking the user.
func GetCredentials(dormaHost string) (string, string, error) {
	//TODO handle empty config dir parameter

	credentialsFile := path.Join(ConfigDir, "host-credentials")
	credentials, err := readHostCredentials(credentialsFile)
	if err != nil {
		return "", "", err
	}

	if c, ok := credentials[dormaHost]; ok {
		return c.User, c.Pass, nil
	}

	fmt.Println(fmt.Sprintf("No credentials for host %q available. Please enter host below:", dormaHost))
	fmt.Print("User> ")
	user, err := readString()
	if err != nil {
		return "", "", err
	}

	fmt.Print("Pass> ")
	pass, err := readPassword()
	if err != nil {
		return "", "", err
	}

	credentials[dormaHost] = credential{User: user, Pass: pass}
	writeHostCredentials(credentialsFile, credentials)

	return user, pass, nil
}

func readHostCredentials(file string) (map[string]credential, error) {
	data, err := ioutil.ReadFile(file)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]credential), nil
		}
		return nil, err
	}

	var credentials map[string]credential
	if err := json.Unmarshal(data, &credentials); err != nil {
		return nil, err
	}

	return credentials, nil
}

func writeHostCredentials(file string, hosts map[string]credential) error {
	if err := os.MkdirAll(path.Dir(file), os.ModePerm); err != nil {
		return err
	}

	data, err := json.Marshal(&hosts)
	if err != nil {
		return err
	}

	return ioutil.WriteFile(file, data, os.ModePerm)
}

func readRune() (rune, error) {
	buffer := []byte{0}
	_, err := os.Stdin.Read(buffer)
	if err != nil {
		return '\000', err
	}
	return rune(buffer[0]), nil
}

func readString() (string, error) {
	var sb strings.Builder
	for {
		r, err := readRune()
		if err != nil {
			return sb.String(), err
		}
		if r == '\n' {
			break
		}
		sb.WriteRune(r)
	}
	return sb.String(), nil
}

func readPassword() (string, error) {
	data, err := terminal.ReadPassword(int(os.Stdin.Fd()))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// FetchDormaEntries returns today's entries available in "Aktuelle Buchungen" in Dorma.
func FetchDormaEntries(dormaHost string, user, pass string) ([]Entry, error) {
	client := &http.Client{
		Transport: ntlmssp.Negotiator{
			RoundTripper: &http.Transport{},
		},
	}

	sessionID, err := login(client, dormaHost, user, pass)
	if err != nil {
		return nil, fmt.Errorf("login failed: %s", err.Error())
	}

	// ignore errors here -> result is already available or it failed anyway
	defer logout(client, dormaHost, user, pass, sessionID)

	entries, err := getEntries(client, dormaHost, user, pass, sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve entries: %s", err.Error())
	}

	return entries, nil
}

func login(client *http.Client, dormaHost, user, pass string) (string, error) {
	request, err := http.NewRequest("GET", fmt.Sprintf(urlDormaLogin, dormaHost), nil)
	if err != nil {
		return "", err
	}
	request.SetBasicAuth(user, pass)

	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	if response.StatusCode != 200 {
		return "", fmt.Errorf("server returned code %d", response.StatusCode)
	}

	var sessionID string
	for _, c := range response.Cookies() {
		if c.Name == sessionCookieName {
			sessionID = c.Value
		}
	}

	if len(sessionID) == 0 {
		return "", fmt.Errorf("missing Cookie " + sessionCookieName)
	}

	return sessionID, nil
}

func logout(client *http.Client, dormaHost, user, pass, sessionID string) error {
	request, err := http.NewRequest("GET", fmt.Sprintf(urlDormaLogout, dormaHost), nil)
	if err != nil {
		return err
	}
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionID})
	request.SetBasicAuth(user, pass)

	response, err := client.Do(request)
	if err != nil {
		return err
	}
	if response.StatusCode != 200 {
		return fmt.Errorf("server returned code %d", response.StatusCode)
	}

	return nil
}

func getEntries(client *http.Client, dormaHost, user, pass, sessionID string) ([]Entry, error) {
	request, err := http.NewRequest("GET", fmt.Sprintf(urlDormaEntries, dormaHost), nil)
	if err != nil {
		return nil, err
	}
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionID})
	request.SetBasicAuth(user, pass)

	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("server returned code %d", response.StatusCode)
	}

	buffer := bytes.NewBuffer([]byte{})
	if _, err := io.Copy(buffer, response.Body); err != nil {
		return nil, err
	}

	body := buffer.String()

	pattern := regexp.MustCompile(`<td class="td-tabelle">\s*(&nbsp;)?(\d*)\.?(\d*)\.?(\d*)\s*</td>\s*<td class="td-tabelle">\s*(\d+):(\d+)\s*</td>\s*<td class="td-tabelle">\s*([^<]+?)\s*</td>`)

	// the date is often omitted for repeated values -> save last date to set it to empty entries
	var lastYear, lastMonth, lastDay int

	matches := pattern.FindAllStringSubmatch(body, -1)
	entries := make([]Entry, 0)
	for _, m := range matches {
		if len(m) != 8 {
			continue
		}

		if len(m[2]) > 0 && len(m[3]) > 0 && len(m[4]) > 0 {
			day, _ := strconv.Atoi(m[2])
			month, _ := strconv.Atoi(m[3])
			year, _ := strconv.Atoi(m[4])
			lastYear = year
			lastMonth = month
			lastDay = day
		}

		if lastYear == 0 {
			return nil, fmt.Errorf("missing date for first entry")
		}

		hour, _ := strconv.Atoi(m[5])
		minute, _ := strconv.Atoi(m[6])
		date := time.Date(lastYear, time.Month(lastMonth), lastDay, hour, minute, 0, 0, time.Local)

		typeStr := m[7]
		var entryType EntryType
		if strings.Contains(strings.ToLower(typeStr), "kommen") {
			entryType = EntryTypeCome
		} else if strings.Contains(strings.ToLower(typeStr), "gehen") {
			entryType = EntryTypeLeave
		} else {
			return nil, fmt.Errorf("cannot parse entry type from %q", typeStr)
		}

		entries = append(entries, Entry{Time: date, Type: entryType})
	}

	return entries, nil
}