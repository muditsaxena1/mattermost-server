// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package api4

import (
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"

	"github.com/mattermost/mattermost-server/v6/audit"
	"github.com/mattermost/mattermost-server/v6/model"
	"github.com/mattermost/mattermost-server/v6/shared/mlog"
)

func (api *API) InitUpload() {
	api.BaseRoutes.Uploads.Handle("", api.ApiSessionRequired(createUpload)).Methods("POST")
	api.BaseRoutes.Upload.Handle("", api.ApiSessionRequired(getUpload)).Methods("GET")
	api.BaseRoutes.Upload.Handle("", api.ApiSessionRequired(uploadData)).Methods("POST")
}

func createUpload(c *Context, w http.ResponseWriter, r *http.Request) {
	if !*c.App.Config().FileSettings.EnableFileAttachments {
		c.Err = model.NewAppError("createUpload",
			"api.file.attachments.disabled.app_error",
			nil, "", http.StatusNotImplemented)
		return
	}

	us := model.UploadSessionFromJson(r.Body)
	if us == nil {
		c.SetInvalidParam("upload")
		return
	}

	// these are not supported for client uploads; shared channels only.
	us.RemoteId = ""
	us.ReqFileId = ""

	auditRec := c.MakeAuditRecord("createUpload", audit.Fail)
	defer c.LogAuditRec(auditRec)
	auditRec.AddMeta("upload", us)

	if us.Type == model.UploadTypeImport {
		if !c.IsSystemAdmin() {
			c.SetPermissionError(model.PermissionManageSystem)
			return
		}
	} else {
		if !c.App.SessionHasPermissionToChannel(*c.AppContext.Session(), us.ChannelId, model.PermissionUploadFile) {
			c.SetPermissionError(model.PermissionUploadFile)
			return
		}
		us.Type = model.UploadTypeAttachment
	}

	us.Id = model.NewId()
	if c.AppContext.Session().UserId != "" {
		us.UserId = c.AppContext.Session().UserId
	}
	us, err := c.App.CreateUploadSession(us)
	if err != nil {
		c.Err = err
		return
	}

	auditRec.Success()
	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(us); err != nil {
		mlog.Warn("Error while writing response", mlog.Err(err))
	}
}

func getUpload(c *Context, w http.ResponseWriter, r *http.Request) {
	c.RequireUploadId()
	if c.Err != nil {
		return
	}

	us, err := c.App.GetUploadSession(c.Params.UploadId)
	if err != nil {
		c.Err = err
		return
	}

	if us.UserId != c.AppContext.Session().UserId && !c.IsSystemAdmin() {
		c.Err = model.NewAppError("getUpload", "api.upload.get_upload.forbidden.app_error", nil, "", http.StatusForbidden)
		return
	}

	if err := json.NewEncoder(w).Encode(us); err != nil {
		mlog.Warn("Error while writing response", mlog.Err(err))
	}
}

func uploadData(c *Context, w http.ResponseWriter, r *http.Request) {
	if !*c.App.Config().FileSettings.EnableFileAttachments {
		c.Err = model.NewAppError("uploadData", "api.file.attachments.disabled.app_error",
			nil, "", http.StatusNotImplemented)
		return
	}

	c.RequireUploadId()
	if c.Err != nil {
		return
	}

	auditRec := c.MakeAuditRecord("uploadData", audit.Fail)
	defer c.LogAuditRec(auditRec)
	auditRec.AddMeta("upload_id", c.Params.UploadId)

	us, err := c.App.GetUploadSession(c.Params.UploadId)
	if err != nil {
		c.Err = err
		return
	}

	if us.Type == model.UploadTypeImport {
		if !c.IsSystemAdmin() {
			c.SetPermissionError(model.PermissionManageSystem)
			return
		}
	} else {
		if us.UserId != c.AppContext.Session().UserId || !c.App.SessionHasPermissionToChannel(*c.AppContext.Session(), us.ChannelId, model.PermissionUploadFile) {
			c.SetPermissionError(model.PermissionUploadFile)
			return
		}
	}

	info, err := doUploadData(c, us, r)
	if err != nil {
		c.Err = err
		return
	}

	auditRec.Success()

	if info == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if err := json.NewEncoder(w).Encode(info); err != nil {
		mlog.Warn("Error while writing response", mlog.Err(err))
	}
}

func doUploadData(c *Context, us *model.UploadSession, r *http.Request) (*model.FileInfo, *model.AppError) {
	boundary, parseErr := parseMultipartRequestHeader(r)
	if parseErr != nil && !errors.Is(parseErr, http.ErrNotMultipart) {
		return nil, model.NewAppError("uploadData", "api.upload.upload_data.invalid_content_type",
			nil, parseErr.Error(), http.StatusBadRequest)
	}

	var rd io.Reader
	if boundary != "" {
		mr := multipart.NewReader(r.Body, boundary)
		p, partErr := mr.NextPart()
		if partErr != nil {
			return nil, model.NewAppError("uploadData", "api.upload.upload_data.multipart_error",
				nil, partErr.Error(), http.StatusBadRequest)
		}
		rd = p
	} else {
		if r.ContentLength > (us.FileSize - us.FileOffset) {
			return nil, model.NewAppError("uploadData", "api.upload.upload_data.invalid_content_length",
				nil, "", http.StatusBadRequest)
		}
		rd = r.Body
	}

	return c.App.UploadData(c.AppContext, us, rd)
}
