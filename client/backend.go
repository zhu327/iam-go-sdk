/*
 * TencentBlueKing is pleased to support the open source community by making
 * 蓝鲸智云-权限中心Go SDK(iam-go-sdk) available.
 * Copyright (C) 2017-2021 THL A29 Limited, a Tencent company. All rights reserved.
 * Licensed under the MIT License (the "License"); you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at http://opensource.org/licenses/MIT
 * Unless required by applicable law or agreed to in writing, software distributed under the License is distributed on
 * an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the License for the
 * specific language governing permissions and limitations under the License.
 */

package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/TencentBlueKing/gopkg/conv"
	"github.com/parnurzeal/gorequest"

	"github.com/TencentBlueKing/iam-go-sdk/logger"
	"github.com/TencentBlueKing/iam-go-sdk/util"
)

const (
	bkIAMVersion = "1"
)

// Method is the type of http method
type Method string

var (
	// POST http post
	POST Method = "POST"
	// GET http get
	GET Method = "GET"
)

// IAMBackendBaseResponse is the struct of iam backend response
type IAMBackendBaseResponse struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// Error will check if the response with error
func (r *IAMBackendBaseResponse) Error() error {
	if r.Code == 0 {
		return nil
	}

	return fmt.Errorf("response error[code=`%d`,  message=`%s`]", r.Code, r.Message)
}

// String will return the detail text of the response
func (r *IAMBackendBaseResponse) String() string {
	return fmt.Sprintf("response[code=`%d`, message=`%s`, data=`%s`]", r.Code, r.Message, conv.BytesToString(r.Data))
}

// IAMBackendClient is the interface of iam backend client
type IAMBackendClient interface {
	Ping() error
	GetToken() (token string, err error)

	PolicyQuery(body interface{}) (map[string]interface{}, error)
	PolicyQueryByActions(body interface{}) ([]map[string]interface{}, error)

	V2PolicyQuery(system string, body interface{}) (data map[string]interface{}, err error)
	V2PolicyQueryByActions(system string, body interface{}) (data []map[string]interface{}, err error)
	V2PolicyAuth(system string, body interface{}) (data map[string]interface{}, err error)

	PolicyAuth(body interface{}) (data map[string]interface{}, err error)
	PolicyAuthByResources(body interface{}) (data map[string]interface{}, err error)
	PolicyAuthByActions(body interface{}) (data map[string]interface{}, err error)

	PolicyGet(policyID int64) (data map[string]interface{}, err error)
	PolicyList(body interface{}) (data map[string]interface{}, err error)
	PolicySubjects(policyIDs []int64) (data []map[string]interface{}, err error)

	GetApplyURL(body interface{}) (string, error)
}

type iamBackendClient struct {
	Host         string
	IsAPIGateway bool

	System    string
	appCode   string
	appSecret string

	isApiDebugEnabled bool
	isApiForceEnabled bool
}

// NewIAMBackendClient will create a iam backend client
func NewIAMBackendClient(host string, isAPIGateway bool, system string, appCode string, appSecret string) IAMBackendClient {
	host = strings.TrimRight(host, "/")
	return &iamBackendClient{
		Host:         host,
		IsAPIGateway: isAPIGateway,

		System:    system,
		appCode:   appCode,
		appSecret: appSecret,

		// will add ?debug=true in url, for debug api/policy, show the details
		isApiDebugEnabled: os.Getenv("IAM_API_DEBUG") == "true" || os.Getenv("BKAPP_IAM_API_DEBUG") == "true",
		// will add ?force=true in url, for api/policy run without cache(all data from database)
		isApiForceEnabled: os.Getenv("IAM_API_FORCE") == "true" || os.Getenv("BKAPP_IAM_API_FORCE") == "true",
	}
}

func (c *iamBackendClient) call(
	method Method, path string,
	data interface{},
	timeout int64,
	responseData interface{},
) error {
	callTimeout := time.Duration(timeout) * time.Second
	if timeout == 0 {
		callTimeout = defaultTimeout
	}

	headers := map[string]string{
		"X-Bk-IAM-Version": bkIAMVersion,
	}

	if c.IsAPIGateway {
		auth, err := json.Marshal(map[string]string{
			"bk_app_code":   c.appCode,
			"bk_app_secret": c.appSecret,
		})
		if err != nil {
			return fmt.Errorf("generate apigateway call header fail. err=`%s`", err)
		}

		headers["X-Bkapi-Authorization"] = conv.BytesToString(auth)
	} else {
		headers["X-BK-APP-CODE"] = c.appCode
		headers["X-BK-APP-SECRET"] = c.appSecret
	}

	url := fmt.Sprintf("%s%s", c.Host, path)
	start := time.Now()
	callbackFunc := NewMetricCallback("IAMBackend", start)

	logger.Debugf("do http request: method=`%s`, url=`%s`, data=`%s`", method, url, data)

	// request := gorequest.New().Timeout(callTimeout).Post(url).Type("json")
	request := gorequest.New().Timeout(callTimeout).Type("json")
	switch method {
	case POST:
		request = request.Post(url).Send(data)
	case GET:
		request = request.Get(url).Query(data)
	}

	if c.isApiDebugEnabled {
		request.QueryData.Add("debug", "true")
	}
	if c.isApiForceEnabled {
		request.QueryData.Add("force", "true")
	}

	// set headers
	for key, value := range headers {
		request.Header.Set(key, value)
	}

	// do request
	baseResult := IAMBackendBaseResponse{}
	resp, respBody, errs := request.
		EndStruct(&baseResult, callbackFunc)

	duration := time.Since(start)

	logFailHTTPRequest(request, resp, respBody, errs, &baseResult)

	logger.Debugf("http request result: %+v", baseResult.String())
	logger.Debugf("http request took %v ms", float64(duration/time.Millisecond))
	logger.Debugf("http response: status_code=%s, body=%+v", resp.StatusCode, conv.BytesToString(respBody))

	if len(errs) != 0 {
		return fmt.Errorf("gorequest errors=`%s`", errs)
	}
	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("gorequest statusCode is %d not 200", resp.StatusCode)
		if baseResult.Message != "" {
			err = fmt.Errorf("%w. response body.code: %d, message:%s", err, baseResult.Code, baseResult.Message)
		}

		return err
	}

	if baseResult.Code != 0 {
		return fmt.Errorf("response body.code: %d, message:%s", baseResult.Code, baseResult.Message)
	}

	err := json.Unmarshal(baseResult.Data, responseData)
	if err != nil {
		return fmt.Errorf("http request response body data not valid: %w, data=`%v`", err, baseResult.Data)
	}
	return nil
}

func (c *iamBackendClient) callWithReturnMapData(
	method Method, path string,
	data interface{},
	timeout int64,
) (map[string]interface{}, error) {
	var responseData map[string]interface{}
	err := c.call(method, path, data, timeout, &responseData)
	if err != nil {
		return map[string]interface{}{}, err
	}
	return responseData, nil
}

func (c *iamBackendClient) callWithReturnSliceMapData(
	method Method, path string,
	data interface{},
	timeout int64,
) ([]map[string]interface{}, error) {
	var responseData []map[string]interface{}
	err := c.call(method, path, data, timeout, &responseData)
	if err != nil {
		return []map[string]interface{}{}, err
	}
	return responseData, nil
}

// Ping will check the iam backend service is ping-able
func (c *iamBackendClient) Ping() (err error) {
	url := fmt.Sprintf("%s%s", c.Host, "/ping")

	resp, _, errs := gorequest.New().Timeout(defaultTimeout).Get(url).EndBytes()
	if len(errs) != 0 {
		return fmt.Errorf("ping fail! errs=%v", errs)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ping fail! status_code=%d", resp.StatusCode)
	}
	return nil
}

// GetToken will get the token of system, use for callback requests basic auth
func (c *iamBackendClient) GetToken() (token string, err error) {
	path := fmt.Sprintf("/api/v1/model/systems/%s/token", c.System)
	data, err := c.callWithReturnMapData(GET, path, map[string]interface{}{}, 10)
	if err != nil {
		return "", err
	}
	tokenI, ok := data["token"]
	if !ok {
		return "", errors.New("no token in response body")
	}
	token, ok = tokenI.(string)
	if !ok {
		return "", errors.New("token is not a valid string")
	}
	return token, nil
}

// PolicyQuery will do policy query
func (c *iamBackendClient) PolicyQuery(body interface{}) (data map[string]interface{}, err error) {
	path := "/api/v1/policy/query"
	data, err = c.callWithReturnMapData(POST, path, body, 10)
	return
}

// V2PolicyQuery will do policy query
func (c *iamBackendClient) V2PolicyQuery(system string, body interface{}) (data map[string]interface{}, err error) {
	path := "/api/v2/policy/systems/" + system + "/query/"
	data, err = c.callWithReturnMapData(POST, path, body, 10)
	return
}

// PolicyQueryByActions will do policy query by actions
func (c *iamBackendClient) PolicyQueryByActions(body interface{}) (data []map[string]interface{}, err error) {
	path := "/api/v1/policy/query_by_actions"
	data, err = c.callWithReturnSliceMapData(POST, path, body, 10)
	return
}

// V2PolicyQueryByActions will do policy query by actions
func (c *iamBackendClient) V2PolicyQueryByActions(system string, body interface{}) (data []map[string]interface{}, err error) {
	path := "/api/v2/policy/systems/" + system + "/query_by_actions/"
	data, err = c.callWithReturnSliceMapData(POST, path, body, 10)
	return
}

// PolicyAuth will do policy auth
func (c *iamBackendClient) PolicyAuth(body interface{}) (data map[string]interface{}, err error) {
	path := "/api/v1/policy/auth"
	data, err = c.callWithReturnMapData(POST, path, body, 10)
	return
}

// V2PolicyAuth will do policy auth
func (c *iamBackendClient) V2PolicyAuth(system string, body interface{}) (data map[string]interface{}, err error) {
	path := "/api/v2/policy/systems/" + system + "/auth/"
	data, err = c.callWithReturnMapData(POST, path, body, 10)
	return
}

// PolicyAuthByResources will do policy auth by resources
func (c *iamBackendClient) PolicyAuthByResources(body interface{}) (data map[string]interface{}, err error) {
	path := "/api/v1/policy/auth_by_resources"
	data, err = c.callWithReturnMapData(POST, path, body, 10)
	return
}

// PolicyAuthByActions will do policy auth by actions
func (c *iamBackendClient) PolicyAuthByActions(body interface{}) (data map[string]interface{}, err error) {
	path := "/api/v1/policy/auth_by_actions"
	data, err = c.callWithReturnMapData(POST, path, body, 10)
	return
}

// PolicyGet will get the policy detail by id
func (c *iamBackendClient) PolicyGet(policyID int64) (data map[string]interface{}, err error) {
	path := fmt.Sprintf("/api/v1/systems/%s/policies/%d", c.System, policyID)
	data, err = c.callWithReturnMapData(GET, path, map[string]interface{}{}, 10)
	return
}

// PolicyList will list all the policy
func (c *iamBackendClient) PolicyList(body interface{}) (data map[string]interface{}, err error) {
	path := fmt.Sprintf("/api/v1/systems/%s/policies", c.System)
	data, err = c.callWithReturnMapData(GET, path, body, 10)
	return
}

// PolicySubjects will query the subject of each policy
func (c *iamBackendClient) PolicySubjects(policyIDs []int64) (data []map[string]interface{}, err error) {
	path := fmt.Sprintf("/api/v1/systems/%s/policies/-/subjects", c.System)

	body := map[string]interface{}{
		"ids": util.Int64ArrayToString(policyIDs, ","),
	}
	data, err = c.callWithReturnSliceMapData(GET, path, body, 10)
	return
}

// GetApplyURL will get apply url from iam saas
func (c *iamBackendClient) GetApplyURL(body interface{}) (url string, err error) {
	path := "/api/v1/open/application/"
	data, err := c.callWithReturnMapData(POST, path, body, 10)
	if err != nil {
		return "", err
	}

	urlI, ok := data["url"]
	if !ok {
		return "", errors.New("no url in response body")
	}
	url, ok = urlI.(string)
	if !ok {
		return "", errors.New("url is not a valid string")
	}
	return url, nil
}
