package cdn

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/dgrijalva/jwt-go"
	"github.com/mitchellh/hashstructure"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/h2non/gock.v1"

	"github.com/ovh/cds/engine/api/test/assets"
	"github.com/ovh/cds/engine/authentication"
	"github.com/ovh/cds/engine/cdn/item"
	"github.com/ovh/cds/engine/cdn/redis"
	cdntest "github.com/ovh/cds/engine/cdn/test"
	"github.com/ovh/cds/engine/test"
	"github.com/ovh/cds/sdk"
	"github.com/ovh/cds/sdk/cdsclient"
	"github.com/ovh/cds/sdk/log"
	"github.com/ovh/cds/sdk/log/hook"
)

func TestMarkItemToDeleteHandler(t *testing.T) {
	s, db := newTestService(t)
	s.Cfg.EnableLogProcessing = true
	cdntest.ClearItem(t, context.TODO(), s.Mapper, db)

	item1 := sdk.CDNItem{
		ID:   sdk.UUID(),
		Type: sdk.CDNTypeItemStepLog,
		APIRef: sdk.CDNLogAPIRef{
			RunID:      1,
			WorkflowID: 1,
		},
		APIRefHash: sdk.RandomString(10),
	}
	require.NoError(t, item.Insert(context.TODO(), s.Mapper, db, &item1))
	item2 := sdk.CDNItem{
		ID:   sdk.UUID(),
		Type: sdk.CDNTypeItemStepLog,
		APIRef: sdk.CDNLogAPIRef{
			RunID:      2,
			WorkflowID: 2,
		},
		APIRefHash: sdk.RandomString(10),
	}
	require.NoError(t, item.Insert(context.TODO(), s.Mapper, db, &item2))

	item3 := sdk.CDNItem{
		ID:   sdk.UUID(),
		Type: sdk.CDNTypeItemStepLog,
		APIRef: sdk.CDNLogAPIRef{
			RunID:      3,
			WorkflowID: 2,
		},
		APIRefHash: sdk.RandomString(10),
	}
	require.NoError(t, item.Insert(context.TODO(), s.Mapper, db, &item3))

	vars := map[string]string{}
	uri := s.Router.GetRoute("POST", s.markItemToDeleteHandler, vars)
	require.NotEmpty(t, uri)
	req := newRequest(t, "POST", uri, sdk.CDNMarkDelete{RunID: 2})

	rec := httptest.NewRecorder()
	s.Router.Mux.ServeHTTP(rec, req)
	require.Equal(t, 204, rec.Code)

	item3DB, err := item.LoadByID(context.TODO(), s.Mapper, db, item3.ID)
	require.NoError(t, err)
	require.False(t, item3DB.ToDelete)

	item2DB, err := item.LoadByID(context.TODO(), s.Mapper, db, item2.ID)
	require.NoError(t, err)
	require.True(t, item2DB.ToDelete)

	item1DB, err := item.LoadByID(context.TODO(), s.Mapper, db, item1.ID)
	require.NoError(t, err)
	require.False(t, item1DB.ToDelete)

	vars2 := map[string]string{}
	uri2 := s.Router.GetRoute("POST", s.markItemToDeleteHandler, vars2)
	require.NotEmpty(t, uri2)
	req2 := newRequest(t, "POST", uri, sdk.CDNMarkDelete{WorkflowID: 1})

	rec2 := httptest.NewRecorder()
	s.Router.Mux.ServeHTTP(rec2, req2)
	require.Equal(t, 204, rec2.Code)

	item3DBAfter, err := item.LoadByID(context.TODO(), s.Mapper, db, item3.ID)
	require.NoError(t, err)
	require.False(t, item3DBAfter.ToDelete)

	item2DBAfter, err := item.LoadByID(context.TODO(), s.Mapper, db, item2.ID)
	require.NoError(t, err)
	require.True(t, item2DBAfter.ToDelete)

	item1DBAfter, err := item.LoadByID(context.TODO(), s.Mapper, db, item1.ID)
	require.NoError(t, err)
	require.True(t, item1DBAfter.ToDelete)
}

func TestGetItemLogsDownloadHandler(t *testing.T) {
	projectKey := sdk.RandomString(10)
	// Create cdn service with need storage and test item
	s, _ := newTestService(t)
	s.Client = cdsclient.New(cdsclient.Config{Host: "http://lolcat.api", InsecureSkipVerifyTLS: false})
	gock.InterceptClient(s.Client.(cdsclient.Raw).HTTPClient())
	gock.New("http://lolcat.api").Get("/project/" + projectKey + "/workflows/MyWorkflow/log/access").Reply(http.StatusOK).JSON(nil)

	cdnUnits := newRunningStorageUnits(t, s.Mapper, s.mustDBWithCtx(context.TODO()))

	s.Units = cdnUnits

	hm := handledMessage{
		Msg: hook.Message{
			Full: "this is a message",
		},
		Status: sdk.StatusSuccess,
		Line:   2,
		Signature: log.Signature{
			ProjectKey:   projectKey,
			WorkflowID:   1,
			WorkflowName: "MyWorkflow",
			RunID:        1,
			NodeRunID:    1,
			NodeRunName:  "MyPipeline",
			JobName:      "MyJob",
			JobID:        1,
			Worker: &log.SignatureWorker{
				StepName:  "script1",
				StepOrder: 1,
			},
		},
	}

	content := buildMessage(hm)
	err := s.storeLogs(context.TODO(), sdk.CDNTypeItemStepLog, hm.Signature, hm.Status, content, hm.Line)
	require.NoError(t, err)

	signer, err := authentication.NewSigner("cdn-test", test.SigningKey)
	require.NoError(t, err)
	s.Common.ParsedAPIPublicKey = signer.GetVerifyKey()
	jwtToken := jwt.NewWithClaims(jwt.SigningMethodRS512, sdk.AuthSessionJWTClaims{
		ID: sdk.UUID(),
		StandardClaims: jwt.StandardClaims{
			Issuer:    "test",
			Subject:   sdk.UUID(),
			Id:        sdk.UUID(),
			IssuedAt:  time.Now().Unix(),
			ExpiresAt: time.Now().Add(time.Minute).Unix(),
		},
	})
	jwtTokenRaw, err := signer.SignJWT(jwtToken)
	require.NoError(t, err)

	apiRef := sdk.CDNLogAPIRef{
		ProjectKey:     hm.Signature.ProjectKey,
		WorkflowName:   hm.Signature.WorkflowName,
		WorkflowID:     hm.Signature.WorkflowID,
		RunID:          hm.Signature.RunID,
		NodeRunName:    hm.Signature.NodeRunName,
		NodeRunID:      hm.Signature.NodeRunID,
		NodeRunJobName: hm.Signature.JobName,
		NodeRunJobID:   hm.Signature.JobID,
		StepName:       hm.Signature.Worker.StepName,
		StepOrder:      hm.Signature.Worker.StepOrder,
	}
	apiRefHashU, err := hashstructure.Hash(apiRef, nil)
	require.NoError(t, err)
	apiRefHash := strconv.FormatUint(apiRefHashU, 10)

	uri := s.Router.GetRoute("GET", s.getItemDownloadHandler, map[string]string{
		"type":   string(sdk.CDNTypeItemStepLog),
		"apiRef": apiRefHash,
	})
	require.NotEmpty(t, uri)
	req := assets.NewJWTAuthentifiedRequest(t, jwtTokenRaw, "GET", uri, nil)
	rec := httptest.NewRecorder()
	s.Router.Mux.ServeHTTP(rec, req)
	require.Equal(t, 200, rec.Code)

	assert.Equal(t, "[EMERGENCY] this is a message\n", string(rec.Body.Bytes()))
}

func TestGetItemLogsLinesHandler(t *testing.T) {

	projectKey := sdk.RandomString(10)

	// Create cdn service with need storage and test item
	s, _ := newTestService(t)
	s.Client = cdsclient.New(cdsclient.Config{Host: "http://lolcat.api", InsecureSkipVerifyTLS: false})
	gock.InterceptClient(s.Client.(cdsclient.Raw).HTTPClient())
	gock.New("http://lolcat.api").Get("/project/" + projectKey + "/workflows/MyWorkflow/log/access").Reply(http.StatusOK).JSON(nil)

	cdnUnits := newRunningStorageUnits(t, s.Mapper, s.mustDBWithCtx(context.TODO()))

	s.Units = cdnUnits

	hm := handledMessage{
		Msg: hook.Message{
			Full: "this is a message",
		},
		Status: sdk.StatusSuccess,
		Line:   2,
		Signature: log.Signature{
			ProjectKey:   projectKey,
			WorkflowID:   1,
			WorkflowName: "MyWorkflow",
			RunID:        1,
			NodeRunID:    1,
			NodeRunName:  "MyPipeline",
			JobName:      "MyJob",
			JobID:        1,
			Worker: &log.SignatureWorker{
				StepName:  "script1",
				StepOrder: 1,
			},
		},
	}

	content := buildMessage(hm)
	err := s.storeLogs(context.TODO(), sdk.CDNTypeItemStepLog, hm.Signature, hm.Status, content, hm.Line)
	require.NoError(t, err)

	signer, err := authentication.NewSigner("cdn-test", test.SigningKey)
	require.NoError(t, err)
	s.Common.ParsedAPIPublicKey = signer.GetVerifyKey()
	jwtToken := jwt.NewWithClaims(jwt.SigningMethodRS512, sdk.AuthSessionJWTClaims{
		ID: sdk.UUID(),
		StandardClaims: jwt.StandardClaims{
			Issuer:    "test",
			Subject:   sdk.UUID(),
			Id:        sdk.UUID(),
			IssuedAt:  time.Now().Unix(),
			ExpiresAt: time.Now().Add(time.Minute).Unix(),
		},
	})
	jwtTokenRaw, err := signer.SignJWT(jwtToken)
	require.NoError(t, err)

	apiRef := sdk.CDNLogAPIRef{
		ProjectKey:     hm.Signature.ProjectKey,
		WorkflowName:   hm.Signature.WorkflowName,
		WorkflowID:     hm.Signature.WorkflowID,
		RunID:          hm.Signature.RunID,
		NodeRunName:    hm.Signature.NodeRunName,
		NodeRunID:      hm.Signature.NodeRunID,
		NodeRunJobName: hm.Signature.JobName,
		NodeRunJobID:   hm.Signature.JobID,
		StepName:       hm.Signature.Worker.StepName,
		StepOrder:      hm.Signature.Worker.StepOrder,
	}
	apiRefHashU, err := hashstructure.Hash(apiRef, nil)
	require.NoError(t, err)
	apiRefHash := strconv.FormatUint(apiRefHashU, 10)

	uri := s.Router.GetRoute("GET", s.getItemLogsLinesHandler, map[string]string{
		"type":   string(sdk.CDNTypeItemStepLog),
		"apiRef": apiRefHash,
	})
	require.NotEmpty(t, uri)
	req := assets.NewJWTAuthentifiedRequest(t, jwtTokenRaw, "GET", uri, nil)
	rec := httptest.NewRecorder()
	s.Router.Mux.ServeHTTP(rec, req)
	require.Equal(t, 200, rec.Code)

	var lines []redis.Line
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &lines))
	require.Len(t, lines, 1)
	require.Equal(t, int64(2), lines[0].Number)
	require.Equal(t, "[EMERGENCY] this is a message\n", lines[0].Value)
}

func TestGetItemLogsStreamHandler(t *testing.T) {
	cfg := test.LoadTestingConf(t, sdk.TypeCDN)

	projectKey := sdk.RandomString(10)

	// Create cdn service with need storage and test item
	s, db := newTestService(t)
	require.NoError(t, s.initWebsocket())
	ts := httptest.NewServer(s.Router.Mux)

	s.Client = cdsclient.New(cdsclient.Config{Host: "http://lolcat.api", InsecureSkipVerifyTLS: false})
	gock.InterceptClient(s.Client.(cdsclient.Raw).HTTPClient())
	gock.New("http://lolcat.api").Get("/project/" + projectKey + "/workflows/MyWorkflow/log/access").Reply(http.StatusOK).JSON(nil)

	cdnUnits, err := storage.Init(context.TODO(), s.Mapper, db.DbMap, sdk.NewGoRoutines(), storage.Configuration{
		Buffer: storage.BufferConfiguration{
			Name: "redis_buffer",
			Redis: storage.RedisBufferConfiguration{
				Host:     cfg["redisHost"],
				Password: cfg["redisPassword"],
			},
		},
	})
	require.NoError(t, err)
	s.Units = cdnUnits

	signature := log.Signature{
		ProjectKey:   projectKey,
		WorkflowID:   1,
		WorkflowName: "MyWorkflow",
		RunID:        1,
		NodeRunID:    1,
		NodeRunName:  "MyPipeline",
		JobName:      "MyJob",
		JobID:        1,
		Worker: &log.SignatureWorker{
			StepName:  "script1",
			StepOrder: 1,
		},
	}

	signer, err := authentication.NewSigner("cdn-test", test.SigningKey)
	require.NoError(t, err)
	s.Common.ParsedAPIPublicKey = signer.GetVerifyKey()
	jwtToken := jwt.NewWithClaims(jwt.SigningMethodRS512, sdk.AuthSessionJWTClaims{
		ID: sdk.UUID(),
		StandardClaims: jwt.StandardClaims{
			Issuer:    "test",
			Subject:   sdk.UUID(),
			Id:        sdk.UUID(),
			IssuedAt:  time.Now().Unix(),
			ExpiresAt: time.Now().Add(time.Minute).Unix(),
		},
	})
	jwtTokenRaw, err := signer.SignJWT(jwtToken)
	require.NoError(t, err)

	apiRef := sdk.CDNLogAPIRef{
		ProjectKey:     signature.ProjectKey,
		WorkflowName:   signature.WorkflowName,
		WorkflowID:     signature.WorkflowID,
		RunID:          signature.RunID,
		NodeRunName:    signature.NodeRunName,
		NodeRunID:      signature.NodeRunID,
		NodeRunJobName: signature.JobName,
		NodeRunJobID:   signature.JobID,
		StepName:       signature.Worker.StepName,
		StepOrder:      signature.Worker.StepOrder,
	}
	apiRefHashU, err := hashstructure.Hash(apiRef, nil)
	require.NoError(t, err)
	apiRefHash := strconv.FormatUint(apiRefHashU, 10)

	var messageCounter int64
	sendMessage := func() {
		hm := handledMessage{
			Msg:       hook.Message{Full: fmt.Sprintf("message %d", messageCounter)},
			Status:    sdk.StatusBuilding,
			Line:      messageCounter,
			Signature: signature,
		}
		content := buildMessage(hm)
		err = s.storeLogs(context.TODO(), sdk.CDNTypeItemStepLog, hm.Signature, hm.Status, content, hm.Line)
		require.NoError(t, err)
		messageCounter++
	}

	client := cdsclient.New(cdsclient.Config{
		Host:                  ts.URL,
		InsecureSkipVerifyTLS: true,
		SessionToken:          jwtTokenRaw,
	})

	uri := s.Router.GetRoute("GET", s.getItemLogsStreamHandler, map[string]string{
		"type":   string(sdk.CDNTypeItemStepLog),
		"apiRef": apiRefHash,
	})
	require.NotEmpty(t, uri)

	// Send some messages before stream
	for i := 0; i < 10; i++ {
		sendMessage()
	}

	// Open connection
	ctx, cancel := context.WithTimeout(context.TODO(), time.Second*10)
	t.Cleanup(func() { cancel() })
	chanMsgReceived := make(chan json.RawMessage, 10)
	chanErrorReceived := make(chan error, 10)
	go func() {
		chanErrorReceived <- client.RequestWebsocket(ctx, sdk.NewGoRoutines(), uri, nil, chanMsgReceived, chanErrorReceived)
	}()

	var lines []redis.Line
	for ctx.Err() == nil && len(lines) < 10 {
		select {
		case <-ctx.Done():
			break
		case err := <-chanErrorReceived:
			require.NoError(t, err)
			break
		case msg := <-chanMsgReceived:
			var line redis.Line
			require.NoError(t, json.Unmarshal(msg, &line))
			lines = append(lines, line)
		}
	}

	require.Len(t, lines, 10)
	require.Equal(t, "[EMERGENCY] message 0\n", lines[0].Value)
	require.Equal(t, int64(0), lines[0].Number)
	require.Equal(t, "[EMERGENCY] message 9\n", lines[9].Value)
	require.Equal(t, int64(9), lines[9].Number)

	// Send some messages
	for i := 0; i < 10; i++ {
		sendMessage()
	}

	for ctx.Err() == nil && len(lines) < 20 {
		select {
		case <-ctx.Done():
			break
		case err := <-chanErrorReceived:
			require.NoError(t, err)
			break
		case msg := <-chanMsgReceived:
			var line redis.Line
			require.NoError(t, json.Unmarshal(msg, &line))
			lines = append(lines, line)
		}
	}

	require.Len(t, lines, 20)
	require.Equal(t, "[EMERGENCY] message 19\n", lines[19].Value)
	require.Equal(t, int64(19), lines[19].Number)

	// Try another connection with offset
	ctx, cancel = context.WithTimeout(context.TODO(), time.Second*10)
	t.Cleanup(func() { cancel() })
	urlWithOffset, err := url.Parse(uri)
	require.NoError(t, err)
	q := urlWithOffset.Query()
	q.Set("offset", "15")
	urlWithOffset.RawQuery = q.Encode()
	go func() {
		chanErrorReceived <- client.RequestWebsocket(ctx, sdk.NewGoRoutines(), urlWithOffset.String(), nil, chanMsgReceived, chanErrorReceived)
	}()

	lines = make([]redis.Line, 0)
	for ctx.Err() == nil && len(lines) < 5 {
		select {
		case <-ctx.Done():
			break
		case err := <-chanErrorReceived:
			require.NoError(t, err)
			break
		case msg := <-chanMsgReceived:
			var line redis.Line
			require.NoError(t, json.Unmarshal(msg, &line))
			lines = append(lines, line)
		}
	}

	require.Len(t, lines, 5)
	require.Equal(t, "[EMERGENCY] message 15\n", lines[0].Value)
	require.Equal(t, int64(15), lines[0].Number)
	require.Equal(t, "[EMERGENCY] message 19\n", lines[4].Value)
	require.Equal(t, int64(19), lines[4].Number)

}
