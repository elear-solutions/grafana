package api

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/grafana/grafana/pkg/api/response"
	"github.com/grafana/grafana/pkg/api/routing"
	"github.com/grafana/grafana/pkg/bus"
	"github.com/grafana/grafana/pkg/models"
	"github.com/grafana/grafana/pkg/services/auth"
	"github.com/grafana/grafana/pkg/services/sqlstore"
	"github.com/grafana/grafana/pkg/services/sqlstore/mockstore"
	"github.com/stretchr/testify/assert"
)

func TestUserTokenAPIEndpoint(t *testing.T) {
	mock := mockstore.NewSQLStoreMock()
	t.Run("When current user attempts to revoke an auth token for a non-existing user", func(t *testing.T) {
		cmd := models.RevokeAuthTokenCmd{AuthTokenId: 2}
		mock.ExpectedError = models.ErrUserNotFound
		revokeUserAuthTokenScenario(t, "Should return not found when calling POST on", "/api/user/revoke-auth-token",
			"/api/user/revoke-auth-token", cmd, 200, func(sc *scenarioContext) {
				sc.fakeReqWithParams("POST", sc.url, map[string]string{}).exec()
				assert.Equal(t, 404, sc.resp.Code)
			}, mock)
	})

	t.Run("When current user gets auth tokens for a non-existing user", func(t *testing.T) {
		mock := &mockstore.SQLStoreMock{
			ExpectedUser:  &models.User{Id: 200},
			ExpectedError: models.ErrUserNotFound,
		}
		getUserAuthTokensScenario(t, "Should return not found when calling GET on", "/api/user/auth-tokens", "/api/user/auth-tokens", 200, func(sc *scenarioContext) {
			sc.fakeReqWithParams("GET", sc.url, map[string]string{}).exec()
			assert.Equal(t, 404, sc.resp.Code)
		}, mock)
	})

	t.Run("When logging out an existing user from all devices", func(t *testing.T) {
		mock := &mockstore.SQLStoreMock{
			ExpectedUser: &models.User{Id: 200},
		}
		logoutUserFromAllDevicesInternalScenario(t, "Should be successful", 1, func(sc *scenarioContext) {
			sc.fakeReqWithParams("POST", sc.url, map[string]string{}).exec()
			assert.Equal(t, 200, sc.resp.Code)
		}, mock)
	})

	t.Run("When logout a non-existing user from all devices", func(t *testing.T) {
		logoutUserFromAllDevicesInternalScenario(t, "Should return not found", testUserID, func(sc *scenarioContext) {
			mock.ExpectedError = models.ErrUserNotFound

			sc.fakeReqWithParams("POST", sc.url, map[string]string{}).exec()
			assert.Equal(t, 404, sc.resp.Code)
		}, mock)
	})

	t.Run("When revoke an auth token for a user", func(t *testing.T) {
		cmd := models.RevokeAuthTokenCmd{AuthTokenId: 2}
		token := &models.UserToken{Id: 1}
		mock := &mockstore.SQLStoreMock{
			ExpectedUser: &models.User{Id: 200},
		}

		revokeUserAuthTokenInternalScenario(t, "Should be successful", cmd, 200, token, func(sc *scenarioContext) {
			sc.userAuthTokenService.GetUserTokenProvider = func(ctx context.Context, userId, userTokenId int64) (*models.UserToken, error) {
				return &models.UserToken{Id: 2}, nil
			}
			sc.fakeReqWithParams("POST", sc.url, map[string]string{}).exec()
			assert.Equal(t, 200, sc.resp.Code)
		}, mock)
	})

	t.Run("When revoke the active auth token used by himself", func(t *testing.T) {
		cmd := models.RevokeAuthTokenCmd{AuthTokenId: 2}
		token := &models.UserToken{Id: 2}
		mock := mockstore.NewSQLStoreMock()
		revokeUserAuthTokenInternalScenario(t, "Should not be successful", cmd, testUserID, token, func(sc *scenarioContext) {
			sc.userAuthTokenService.GetUserTokenProvider = func(ctx context.Context, userId, userTokenId int64) (*models.UserToken, error) {
				return token, nil
			}
			sc.fakeReqWithParams("POST", sc.url, map[string]string{}).exec()
			assert.Equal(t, 400, sc.resp.Code)
		}, mock)
	})

	t.Run("When gets auth tokens for a user", func(t *testing.T) {
		currentToken := &models.UserToken{Id: 1}
		mock := mockstore.NewSQLStoreMock()
		getUserAuthTokensInternalScenario(t, "Should be successful", currentToken, func(sc *scenarioContext) {
			tokens := []*models.UserToken{
				{
					Id:        1,
					ClientIp:  "127.0.0.1",
					UserAgent: "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/72.0.3626.119 Safari/537.36",
					CreatedAt: time.Now().Unix(),
					SeenAt:    time.Now().Unix(),
				},
				{
					Id:        2,
					ClientIp:  "127.0.0.2",
					UserAgent: "Mozilla/5.0 (iPhone; CPU iPhone OS 11_0 like Mac OS X) AppleWebKit/604.1.38 (KHTML, like Gecko) Version/11.0 Mobile/15A372 Safari/604.1",
					CreatedAt: time.Now().Unix(),
					SeenAt:    0,
				},
			}
			sc.userAuthTokenService.GetUserTokensProvider = func(ctx context.Context, userId int64) ([]*models.UserToken, error) {
				return tokens, nil
			}
			sc.fakeReqWithParams("GET", sc.url, map[string]string{}).exec()

			assert.Equal(t, 200, sc.resp.Code)
			result := sc.ToJSON()
			assert.Len(t, result.MustArray(), 2)

			resultOne := result.GetIndex(0)
			assert.Equal(t, tokens[0].Id, resultOne.Get("id").MustInt64())
			assert.True(t, resultOne.Get("isActive").MustBool())
			assert.Equal(t, "127.0.0.1", resultOne.Get("clientIp").MustString())
			assert.Equal(t, time.Unix(tokens[0].CreatedAt, 0).Format(time.RFC3339), resultOne.Get("createdAt").MustString())
			assert.Equal(t, time.Unix(tokens[0].SeenAt, 0).Format(time.RFC3339), resultOne.Get("seenAt").MustString())

			assert.Equal(t, "Other", resultOne.Get("device").MustString())
			assert.Equal(t, "Chrome", resultOne.Get("browser").MustString())
			assert.Equal(t, "72.0", resultOne.Get("browserVersion").MustString())
			assert.Equal(t, "Linux", resultOne.Get("os").MustString())
			assert.Empty(t, resultOne.Get("osVersion").MustString())

			resultTwo := result.GetIndex(1)
			assert.Equal(t, tokens[1].Id, resultTwo.Get("id").MustInt64())
			assert.False(t, resultTwo.Get("isActive").MustBool())
			assert.Equal(t, "127.0.0.2", resultTwo.Get("clientIp").MustString())
			assert.Equal(t, time.Unix(tokens[1].CreatedAt, 0).Format(time.RFC3339), resultTwo.Get("createdAt").MustString())
			assert.Equal(t, time.Unix(tokens[1].CreatedAt, 0).Format(time.RFC3339), resultTwo.Get("seenAt").MustString())

			assert.Equal(t, "iPhone", resultTwo.Get("device").MustString())
			assert.Equal(t, "Mobile Safari", resultTwo.Get("browser").MustString())
			assert.Equal(t, "11.0", resultTwo.Get("browserVersion").MustString())
			assert.Equal(t, "iOS", resultTwo.Get("os").MustString())
			assert.Equal(t, "11.0", resultTwo.Get("osVersion").MustString())
		}, mock)
	})
}

func revokeUserAuthTokenScenario(t *testing.T, desc string, url string, routePattern string, cmd models.RevokeAuthTokenCmd,
	userId int64, fn scenarioFunc, sqlStore sqlstore.Store) {
	t.Run(fmt.Sprintf("%s %s", desc, url), func(t *testing.T) {
		fakeAuthTokenService := auth.NewFakeUserAuthTokenService()

		hs := HTTPServer{
			Bus:              bus.GetBus(),
			AuthTokenService: fakeAuthTokenService,
			SQLStore:         sqlStore,
		}

		sc := setupScenarioContext(t, url)
		sc.userAuthTokenService = fakeAuthTokenService
		sc.defaultHandler = routing.Wrap(func(c *models.ReqContext) response.Response {
			c.Req.Body = mockRequestBody(cmd)
			sc.context = c
			sc.context.UserId = userId
			sc.context.OrgId = testOrgID
			sc.context.OrgRole = models.ROLE_ADMIN

			return hs.RevokeUserAuthToken(c)
		})

		sc.m.Post(routePattern, sc.defaultHandler)

		fn(sc)
	})
}

func getUserAuthTokensScenario(t *testing.T, desc string, url string, routePattern string, userId int64, fn scenarioFunc, sqlStore sqlstore.Store) {
	t.Run(fmt.Sprintf("%s %s", desc, url), func(t *testing.T) {
		fakeAuthTokenService := auth.NewFakeUserAuthTokenService()

		hs := HTTPServer{
			Bus:              bus.GetBus(),
			AuthTokenService: fakeAuthTokenService,
			SQLStore:         sqlStore,
		}

		sc := setupScenarioContext(t, url)
		sc.userAuthTokenService = fakeAuthTokenService
		sc.defaultHandler = routing.Wrap(func(c *models.ReqContext) response.Response {
			sc.context = c
			sc.context.UserId = userId
			sc.context.OrgId = testOrgID
			sc.context.OrgRole = models.ROLE_ADMIN

			return hs.GetUserAuthTokens(c)
		})

		sc.m.Get(routePattern, sc.defaultHandler)

		fn(sc)
	})
}

func logoutUserFromAllDevicesInternalScenario(t *testing.T, desc string, userId int64, fn scenarioFunc, sqlStore sqlstore.Store) {
	t.Run(desc, func(t *testing.T) {
		hs := HTTPServer{
			Bus:              bus.GetBus(),
			AuthTokenService: auth.NewFakeUserAuthTokenService(),
			SQLStore:         sqlStore,
		}

		sc := setupScenarioContext(t, "/")
		sc.defaultHandler = routing.Wrap(func(c *models.ReqContext) response.Response {
			sc.context = c
			sc.context.UserId = testUserID
			sc.context.OrgId = testOrgID
			sc.context.OrgRole = models.ROLE_ADMIN

			return hs.logoutUserFromAllDevicesInternal(context.Background(), userId)
		})

		sc.m.Post("/", sc.defaultHandler)

		fn(sc)
	})
}

func revokeUserAuthTokenInternalScenario(t *testing.T, desc string, cmd models.RevokeAuthTokenCmd, userId int64,
	token *models.UserToken, fn scenarioFunc, sqlStore sqlstore.Store) {
	t.Run(desc, func(t *testing.T) {
		fakeAuthTokenService := auth.NewFakeUserAuthTokenService()

		hs := HTTPServer{
			Bus:              bus.GetBus(),
			AuthTokenService: fakeAuthTokenService,
			SQLStore:         sqlStore,
		}

		sc := setupScenarioContext(t, "/")
		sc.userAuthTokenService = fakeAuthTokenService
		sc.defaultHandler = routing.Wrap(func(c *models.ReqContext) response.Response {
			sc.context = c
			sc.context.UserId = testUserID
			sc.context.OrgId = testOrgID
			sc.context.OrgRole = models.ROLE_ADMIN
			sc.context.UserToken = token

			return hs.revokeUserAuthTokenInternal(c, userId, cmd)
		})
		sc.m.Post("/", sc.defaultHandler)
		fn(sc)
	})
}

func getUserAuthTokensInternalScenario(t *testing.T, desc string, token *models.UserToken, fn scenarioFunc, sqlStore sqlstore.Store) {
	t.Run(desc, func(t *testing.T) {
		fakeAuthTokenService := auth.NewFakeUserAuthTokenService()

		hs := HTTPServer{
			Bus:              bus.GetBus(),
			AuthTokenService: fakeAuthTokenService,
			SQLStore:         sqlStore,
		}

		sc := setupScenarioContext(t, "/")
		sc.userAuthTokenService = fakeAuthTokenService
		sc.defaultHandler = routing.Wrap(func(c *models.ReqContext) response.Response {
			sc.context = c
			sc.context.UserId = testUserID
			sc.context.OrgId = testOrgID
			sc.context.OrgRole = models.ROLE_ADMIN
			sc.context.UserToken = token

			return hs.getUserAuthTokensInternal(c, testUserID)
		})

		sc.m.Get("/", sc.defaultHandler)

		fn(sc)
	})
}
