/*
 * Tencent is pleased to support the open source community by making Polaris available.
 *
 * Copyright (C) 2019 THL A29 Limited, a Tencent company. All rights reserved.
 *
 * Licensed under the BSD 3-Clause License (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * https://opensource.org/licenses/BSD-3-Clause
 *
 * Unless required by applicable law or agreed to in writing, software distributed
 * under the License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR
 * CONDITIONS OF ANY KIND, either express or implied. See the License for the
 * specific language governing permissions and limitations under the License.
 */

package httpserver

import (
	"github.com/emicklei/go-restful"
	"github.com/google/uuid"
	api "github.com/polarismesh/polaris-server/common/api/v1"
	"go.uber.org/zap"
	"strconv"
)

func (h *HTTPServer) getConfigFile(req *restful.Request, rsp *restful.Response) {
	handler := &Handler{req, rsp}

	requestId := handler.HeaderParameter("Request-Id")
	namespace := handler.QueryParameter("namespace")
	group := handler.QueryParameter("group")
	fileName := handler.QueryParameter("fileName")
	clientVersionStr := handler.QueryParameter("version")

	clientVersion, err := strconv.ParseUint(clientVersionStr, 10, 64)
	if err != nil {
		handler.WriteHeaderAndProto(api.NewConfigClientResponseWithMessage(api.BadRequest, "version must be number"))
	}

	response := h.configServer.Service().GetConfigFileForClient(handler.ParseHeaderContext(), namespace, group, fileName, clientVersion)

	configLog.Info("[Config][Client] client get config file success.",
		zap.String("requestId", requestId),
		zap.String("client", req.Request.RemoteAddr),
		zap.String("file", fileName))

	handler.WriteHeaderAndProto(response)
}

func (h *HTTPServer) watchConfigFile(req *restful.Request, rsp *restful.Response) {
	handler := &Handler{req, rsp}

	requestId := req.HeaderParameter("Request-Id")
	clientAddr := req.Request.RemoteAddr

	configLog.Debug("[Config][Client] received client listener request.",
		zap.String("requestId", requestId),
		zap.String("client", clientAddr))

	//1. 解析出客户端监听的配置文件列表
	watchConfigFileRequest := &api.ClientWatchConfigFileRequest{}
	_, err := handler.Parse(watchConfigFileRequest)
	if err != nil {
		configLog.Warn("[Config][Client] parse client watch request error.",
			zap.String("requestId", requestId),
			zap.String("client", req.Request.RemoteAddr))

		handler.WriteHeaderAndProto(api.NewResponseWithMsg(api.ParseException, err.Error()))
		return
	}

	watchFiles := watchConfigFileRequest.WatchFiles
	//2. 检查客户端是否有版本落后
	response := h.configServer.Service().CheckClientConfigFile(handler.ParseHeaderContext(), watchFiles)
	if response != nil {
		handler.WriteHeaderAndProto(response)
		return
	}

	//3. 监听配置变更，hold 请求 30s，30s 内如果有配置发布，则响应请求
	id, _ := uuid.NewUUID()
	clientId := clientAddr + "@" + id.String()[0:8]
	finishChan := make(chan struct{})

	h.addConn(clientId, watchFiles, handler, finishChan)

	<-finishChan
	h.removeConn(clientId, watchFiles)

}
