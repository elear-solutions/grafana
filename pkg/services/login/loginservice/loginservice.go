package loginservice

import (
	"context"
	"errors"

	"github.com/grafana/grafana/pkg/bus"
	"github.com/grafana/grafana/pkg/infra/log"
	"github.com/grafana/grafana/pkg/models"
	"github.com/grafana/grafana/pkg/services/login"
	"github.com/grafana/grafana/pkg/services/quota"
	"github.com/grafana/grafana/pkg/services/sqlstore"
)

var (
	logger = log.New("login.ext_user")
)

func ProvideService(sqlStore sqlstore.Store, bus bus.Bus, quotaService *quota.QuotaService, authInfoService login.AuthInfoService) *Implementation {
	s := &Implementation{
		SQLStore:        sqlStore,
		Bus:             bus,
		QuotaService:    quotaService,
		AuthInfoService: authInfoService,
	}
	return s
}

type Implementation struct {
	SQLStore        sqlstore.Store
	Bus             bus.Bus
	AuthInfoService login.AuthInfoService
	QuotaService    *quota.QuotaService
	TeamSync        login.TeamSyncFunc
}

// CreateUser creates inserts a new one.
func (ls *Implementation) CreateUser(cmd models.CreateUserCommand) (*models.User, error) {
	return ls.SQLStore.CreateUser(context.Background(), cmd)
}

// UpsertUser updates an existing user, or if it doesn't exist, inserts a new one.
func (ls *Implementation) UpsertUser(ctx context.Context, cmd *models.UpsertUserCommand) error {
	extUser := cmd.ExternalUser

	user, err := ls.AuthInfoService.LookupAndUpdate(ctx, &models.GetUserByAuthInfoQuery{
		AuthModule: extUser.AuthModule,
		AuthId:     extUser.AuthId,
		UserId:     extUser.UserId,
		Email:      extUser.Email,
		Login:      extUser.Login,
	})
	if err != nil {
		if !errors.Is(err, models.ErrUserNotFound) {
			return err
		}
		if !cmd.SignupAllowed {
			cmd.ReqContext.Logger.Warn("Not allowing login, user not found in internal user database and allow signup = false", "authmode", extUser.AuthModule)
			return login.ErrSignupNotAllowed
		}

		limitReached, err := ls.QuotaService.QuotaReached(cmd.ReqContext, "user")
		if err != nil {
			cmd.ReqContext.Logger.Warn("Error getting user quota.", "error", err)
			return login.ErrGettingUserQuota
		}
		if limitReached {
			return login.ErrUsersQuotaReached
		}

		cmd.Result, err = ls.createUser(extUser)
		if err != nil {
			return err
		}

		if extUser.AuthModule != "" {
			cmd2 := &models.SetAuthInfoCommand{
				UserId:     cmd.Result.Id,
				AuthModule: extUser.AuthModule,
				AuthId:     extUser.AuthId,
				OAuthToken: extUser.OAuthToken,
			}
			if err := ls.AuthInfoService.SetAuthInfo(ctx, cmd2); err != nil {
				return err
			}
		}
	} else {
		cmd.Result = user

		err = ls.updateUser(ctx, cmd.Result, extUser)
		if err != nil {
			return err
		}

		// Always persist the latest token at log-in
		if extUser.AuthModule != "" && extUser.OAuthToken != nil {
			err = ls.updateUserAuth(ctx, cmd.Result, extUser)
			if err != nil {
				return err
			}
		}

		if extUser.AuthModule == models.AuthModuleLDAP && user.IsDisabled {
			// Re-enable user when it found in LDAP
			if err := ls.SQLStore.DisableUser(ctx, &models.DisableUserCommand{UserId: cmd.Result.Id, IsDisabled: false}); err != nil {
				return err
			}
		}
	}

	if err := ls.syncOrgRoles(ctx, cmd.Result, extUser); err != nil {
		return err
	}

	// Sync isGrafanaAdmin permission
	if extUser.IsGrafanaAdmin != nil && *extUser.IsGrafanaAdmin != cmd.Result.IsAdmin {
		if err := ls.SQLStore.UpdateUserPermissions(cmd.Result.Id, *extUser.IsGrafanaAdmin); err != nil {
			return err
		}
	}

	if ls.TeamSync != nil {
		err := ls.TeamSync(cmd.Result, extUser)
		if err != nil {
			return err
		}
	}

	return nil
}

func (ls *Implementation) DisableExternalUser(ctx context.Context, username string) error {
	// Check if external user exist in Grafana
	userQuery := &models.GetExternalUserInfoByLoginQuery{
		LoginOrEmail: username,
	}

	if err := ls.AuthInfoService.GetExternalUserInfoByLogin(ctx, userQuery); err != nil {
		return err
	}

	userInfo := userQuery.Result
	if userInfo.IsDisabled {
		return nil
	}

	logger.Debug(
		"Disabling external user",
		"user",
		userQuery.Result.Login,
	)

	// Mark user as disabled in grafana db
	disableUserCmd := &models.DisableUserCommand{
		UserId:     userQuery.Result.UserId,
		IsDisabled: true,
	}

	if err := ls.SQLStore.DisableUser(ctx, disableUserCmd); err != nil {
		logger.Debug(
			"Error disabling external user",
			"user",
			userQuery.Result.Login,
			"message",
			err.Error(),
		)
		return err
	}
	return nil
}

// SetTeamSyncFunc sets the function received through args as the team sync function.
func (ls *Implementation) SetTeamSyncFunc(teamSyncFunc login.TeamSyncFunc) {
	ls.TeamSync = teamSyncFunc
}

func (ls *Implementation) createUser(extUser *models.ExternalUserInfo) (*models.User, error) {
	cmd := models.CreateUserCommand{
		Login:        extUser.Login,
		Email:        extUser.Email,
		Name:         extUser.Name,
		SkipOrgSetup: len(extUser.OrgRoles) > 0,
	}

	return ls.CreateUser(cmd)
}

func (ls *Implementation) updateUser(ctx context.Context, user *models.User, extUser *models.ExternalUserInfo) error {
	// sync user info
	updateCmd := &models.UpdateUserCommand{
		UserId: user.Id,
	}

	needsUpdate := false
	if extUser.Login != "" && extUser.Login != user.Login {
		updateCmd.Login = extUser.Login
		user.Login = extUser.Login
		needsUpdate = true
	}

	if extUser.Email != "" && extUser.Email != user.Email {
		updateCmd.Email = extUser.Email
		user.Email = extUser.Email
		needsUpdate = true
	}

	if extUser.Name != "" && extUser.Name != user.Name {
		updateCmd.Name = extUser.Name
		user.Name = extUser.Name
		needsUpdate = true
	}

	if !needsUpdate {
		return nil
	}

	logger.Debug("Syncing user info", "id", user.Id, "update", updateCmd)
	return ls.SQLStore.UpdateUser(ctx, updateCmd)
}

func (ls *Implementation) updateUserAuth(ctx context.Context, user *models.User, extUser *models.ExternalUserInfo) error {
	updateCmd := &models.UpdateAuthInfoCommand{
		AuthModule: extUser.AuthModule,
		AuthId:     extUser.AuthId,
		UserId:     user.Id,
		OAuthToken: extUser.OAuthToken,
	}

	logger.Debug("Updating user_auth info", "user_id", user.Id)
	return ls.AuthInfoService.UpdateAuthInfo(ctx, updateCmd)
}

func (ls *Implementation) syncOrgRoles(ctx context.Context, user *models.User, extUser *models.ExternalUserInfo) error {
	logger.Debug("Syncing organization roles", "id", user.Id, "extOrgRoles", extUser.OrgRoles)

	// don't sync org roles if none is specified
	if len(extUser.OrgRoles) == 0 {
		logger.Debug("Not syncing organization roles since external user doesn't have any")
		return nil
	}

	orgsQuery := &models.GetUserOrgListQuery{UserId: user.Id}
	if err := ls.SQLStore.GetUserOrgList(ctx, orgsQuery); err != nil {
		return err
	}

	handledOrgIds := map[int64]bool{}
	deleteOrgIds := []int64{}

	// update existing org roles
	for _, org := range orgsQuery.Result {
		handledOrgIds[org.OrgId] = true

		extRole := extUser.OrgRoles[org.OrgId]
		if extRole == "" {
			deleteOrgIds = append(deleteOrgIds, org.OrgId)
		} else if extRole != org.Role {
			// update role
			cmd := &models.UpdateOrgUserCommand{OrgId: org.OrgId, UserId: user.Id, Role: extRole}
			if err := ls.SQLStore.UpdateOrgUser(ctx, cmd); err != nil {
				return err
			}
		}
	}

	// add any new org roles
	for orgId, orgRole := range extUser.OrgRoles {
		if _, exists := handledOrgIds[orgId]; exists {
			continue
		}

		// add role
		cmd := &models.AddOrgUserCommand{UserId: user.Id, Role: orgRole, OrgId: orgId}
		err := ls.SQLStore.AddOrgUser(ctx, cmd)
		if err != nil && !errors.Is(err, models.ErrOrgNotFound) {
			return err
		}
	}

	// delete any removed org roles
	for _, orgId := range deleteOrgIds {
		logger.Debug("Removing user's organization membership as part of syncing with OAuth login",
			"userId", user.Id, "orgId", orgId)
		cmd := &models.RemoveOrgUserCommand{OrgId: orgId, UserId: user.Id}
		if err := ls.SQLStore.RemoveOrgUser(ctx, cmd); err != nil {
			if errors.Is(err, models.ErrLastOrgAdmin) {
				logger.Error(err.Error(), "userId", cmd.UserId, "orgId", cmd.OrgId)
				continue
			}

			return err
		}
	}

	// update user's default org if needed
	if _, ok := extUser.OrgRoles[user.OrgId]; !ok {
		for orgId := range extUser.OrgRoles {
			user.OrgId = orgId
			break
		}

		return ls.SQLStore.SetUsingOrg(ctx, &models.SetUsingOrgCommand{
			UserId: user.Id,
			OrgId:  user.OrgId,
		})
	}

	return nil
}
