/**
 * @license
 * Copyright 2020 Dynatrace LLC
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package rest

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"crypto/tls"

	"golang.org/x/net/proxy"

	"github.com/dynatrace-oss/dynatrace-monitoring-as-code/pkg/util"

	. "github.com/dynatrace-oss/dynatrace-monitoring-as-code/pkg/api"
)

//go:generate mockgen -source=client.go -destination=client_mock.go -package=rest -imports .=github.com/dynatrace-oss/dynatrace-monitoring-as-code/pkg/api DynatraceClient

// DynatraceClient provides the functionality for performing basic CRUD operations on any Dynatrace API
// supported by monaco.
// It encapsulates the configuration-specific inconsistencies of certain APIs in one place to provide
// a common interface to work with. After all: A user of DynatraceClient shouldn't care about the
// implementation details of each individual Dynatrace API.
// Its design is intentionally not dependant on the Config and Environment interfaces included in monaco.
// This makes sure, that DynatraceClient can be used as a base for future tooling, which relies on
// a standardized way to access Dynatrace APIs.
type DynatraceClient interface {

	// List lists the available configs for an API.
	// It calls the underlying GET endpoint of the API. E.g. for alerting profiles this would be:
	//    GET <environment-url>/api/config/v1/alertingProfiles
	// The result is expressed using a list of Value (id and name tuples).
	List(a Api) (values []Value, err error)

	// ReadByName reads a Dynatrace config identified by name from the given API.
	// It calls the underlying GET endpoints for the API. E.g. for alerting profiles this would be:
	//    GET <environment-url>/api/config/v1/alertingProfiles ... to get the id of the existing alerting profile
	//    GET <environment-url>/api/config/v1/alertingProfiles/<id> ... to get the alerting profile
	ReadByName(a Api, name string) (json []byte, err error)

	// ReadById reads a Dynatrace config identified by id from the given API.
	// It calls the underlying GET endpoint for the API. E.g. for alerting profiles this would be:
	//    GET <environment-url>/api/config/v1/alertingProfiles/<id> ... to get the alerting profile
	ReadById(a Api, name string) (json []byte, err error)

	// Upsert creates a given Dynatrace config it it doesn't exists and updates it otherwise using its name
	// It calls the underlying GET, POST, and PUT endpoints for the API. E.g. for alerting profiles this would be:
	//    GET <environment-url>/api/config/v1/alertingProfiles ... to check if the config is already available
	//    POST <environment-url>/api/config/v1/alertingProfiles ... afterwards, if the config is not yet available
	//    PUT <environment-url>/api/config/v1/alertingProfiles/<id> ... instead of POST, if the config is already available
	UpsertByName(a Api, name string, payload []byte) (entity DynatraceEntity, err error)

	// Delete removed a given config for a given API using its name.
	// It calls the underlying GET and DELETE endpoints for the API. E.g. for alerting profiles this would be:
	//    GET <environment-url>/api/config/v1/alertingProfiles ... to get the id of the existing config
	//    DELETE <environment-url>/api/config/v1/alertingProfiles/<id> ... to delete the config
	DeleteByName(a Api, name string) error

	// ExistsByName checks if a config with the given name exists for the given API.
	// It cally the underlying GET endpoint for the API. E.g. for alerting profiles this would be:
	//    GET <environment-url>/api/config/v1/alertingProfiles
	ExistsByName(a Api, name string) (exists bool, id string, err error)
}

type dynatraceClientImpl struct {
	environmentUrl string
	token          string
	client         *http.Client
}

/*const (
	PROXY_ADDR = os.Getenv("MONACO_SOCK_PROXY")
	//URL        = "http://skunksworkedp2cg.onion/sites.html"
)*/

// NewDynatraceClient creates a new DynatraceClient
func NewDynatraceClient(environmentUrl, token string) (DynatraceClient, error) {

	if environmentUrl == "" {
		return nil, errors.New("no environment url")
	}

	if token == "" {
		return nil, errors.New("no token")
	}

	parsedUrl, err := url.ParseRequestURI(environmentUrl)
	if err != nil {
		return nil, errors.New("environment url " + environmentUrl + " was not valid")
	}

	if parsedUrl.Scheme != "https" {
		return nil, errors.New("environment url " + environmentUrl + " was not valid")
	}

	if !isNewDynatraceTokenFormat(token) {
		util.Log.Warn("You used an old token format. Please consider switching to the new 1.205+ token format.")
		util.Log.Warn("More information: https://www.dynatrace.com/support/help/dynatrace-api/basics/dynatrace-api-authentication/#-dynatrace-version-1205--token-format")
	}

	httpTransport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}

	val, present := os.LookupEnv("MONACO_SOCKS_PROXY")

	if present {

		dialer, err := proxy.SOCKS5("tcp", val, nil, proxy.Direct)
		if err != nil {
			fmt.Fprintln(os.Stderr, "can't connect to the proxy:", err)
			os.Exit(1)
		}

		httpTransport.Dial = dialer.Dial
	}

	//httpTransport := &http.Transport{
	//	TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	//}
	//httpClient := &http.Client{Transport: httpTransport}

	return &dynatraceClientImpl{
		environmentUrl: environmentUrl,
		token:          token,
		client:         &http.Client{Transport: httpTransport},
	}, nil
}

func isNewDynatraceTokenFormat(token string) bool {
	return strings.HasPrefix(token, "dt0c01.") && strings.Count(token, ".") == 2
}

func (d *dynatraceClientImpl) List(api Api) (values []Value, err error) {

	fullUrl := api.GetUrlFromEnvironmentUrl(d.environmentUrl)
	values, err = getExistingValuesFromEndpoint(d.client, api, fullUrl, d.token)
	return values, err
}

func (d *dynatraceClientImpl) ReadByName(api Api, name string) (json []byte, err error) {

	exists, id, err := d.ExistsByName(api, name)
	if err != nil {
		return nil, err
	}

	if !exists {
		return nil, errors.New("404 - no config found with name " + name)
	}

	return d.ReadById(api, id)
}

func (d *dynatraceClientImpl) ReadById(api Api, id string) (json []byte, err error) {
	fullUrl := api.GetUrlFromEnvironmentUrl(d.environmentUrl) + "/" + id
	response, err := get(d.client, fullUrl, d.token)

	if err != nil {
		return nil, err
	}

	return response.Body, nil
}

func (d *dynatraceClientImpl) DeleteByName(api Api, name string) error {

	return deleteDynatraceObject(d.client, api, name, api.GetUrlFromEnvironmentUrl(d.environmentUrl), d.token)
}

func (d *dynatraceClientImpl) ExistsByName(api Api, name string) (exists bool, id string, err error) {

	existingObjectId, err := getObjectIdIfAlreadyExists(d.client, api, api.GetUrlFromEnvironmentUrl(d.environmentUrl), name, d.token)
	return existingObjectId != "", existingObjectId, err
}

func (d *dynatraceClientImpl) UpsertByName(api Api, name string, payload []byte) (entity DynatraceEntity, err error) {

	fullUrl := api.GetUrlFromEnvironmentUrl(d.environmentUrl)

	if err != nil {
		return DynatraceEntity{}, err
	}

	if api.GetId() == "extension" {
		return uploadExtension(d.client, fullUrl, name, payload, d.token)
	}
	return upsertDynatraceObject(d.client, fullUrl, name, api, payload, d.token)
}
