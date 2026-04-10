// SiYuan - Refactor your thinking
// Copyright (c) 2020-present, b3log.org
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package model

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/88250/gulu"
	"github.com/88250/lute/html"
	"github.com/siyuan-note/dejavu"
	"github.com/siyuan-note/dejavu/cloud"
	"github.com/siyuan-note/logging"
	"github.com/siyuan-note/siyuan/kernel/cache"
	"github.com/siyuan-note/siyuan/kernel/conf"
	"github.com/siyuan-note/siyuan/kernel/filesys"
	"github.com/siyuan-note/siyuan/kernel/sql"
	"github.com/siyuan-note/siyuan/kernel/treenode"
	"github.com/siyuan-note/siyuan/kernel/util"
)

func SyncDataDownload() {
	defer logging.Recover()

	if !checkSync(false, false, true) {
		return
	}

	util.BroadcastByType("main", "syncing", 0, Conf.Language(81), nil)
	if !isProviderOnline(true) { // 这个操作比较耗时，所以要先推送 syncing 事件后再判断网络，这样才能给用户更即时的反馈
		util.BroadcastByType("main", "syncing", 2, Conf.Language(28), nil)
		return
	}

	lockSync()
	defer unlockSync()

	now := util.CurrentTimeMillis()
	Conf.Sync.Synced = now

	err := syncRepoDownload()
	code := 1
	if err != nil {
		code = 2
	}
	util.BroadcastByType("main", "syncing", code, Conf.Sync.Stat, nil)
}

func SyncDataUpload() {
	defer logging.Recover()

	if !checkSync(false, false, true) {
		return
	}

	util.BroadcastByType("main", "syncing", 0, Conf.Language(81), nil)
	if !isProviderOnline(true) { // 这个操作比较耗时，所以要先推送 syncing 事件后再判断网络，这样才能给用户更即时的反馈
		util.BroadcastByType("main", "syncing", 2, Conf.Language(28), nil)
		return
	}

	lockSync()
	defer unlockSync()

	now := util.CurrentTimeMillis()
	Conf.Sync.Synced = now

	err := syncRepoUpload()
	code := 1
	if err != nil {
		code = 2
	}
	util.BroadcastByType("main", "syncing", code, Conf.Sync.Stat, nil)
	return
}

var (
	syncSameCount    = atomic.Int32{}
	autoSyncErrCount = 0
	fixSyncInterval  = 5 * time.Minute

	syncPlanTimeLock = sync.Mutex{}
	syncPlanTime     = time.Now().Add(fixSyncInterval)

	BootSyncSucc = -1 // -1：未执行，0：执行成功，1：执行失败
	ExitSyncSucc = -1
)

func SyncDataJob() {
	syncPlanTimeLock.Lock()
	if time.Now().Before(syncPlanTime) {
		syncPlanTimeLock.Unlock()
		return
	}
	syncPlanTimeLock.Unlock()

	SyncData(false)
}

func BootSyncData() {
	defer logging.Recover()

	if Conf.Sync.Perception {
		connectSyncWebSocket()
	}

	if !checkSync(true, false, false) {
		return
	}

	if !isProviderOnline(false) {
		BootSyncSucc = 1
		util.PushErrMsg(Conf.Language(76), 7000)
		return
	}

	lockSync()
	defer unlockSync()

	util.IncBootProgress(3, "Syncing data from the cloud...")
	BootSyncSucc = 0
	logging.LogInfof("sync before boot")

	now := util.CurrentTimeMillis()
	Conf.Sync.Synced = now
	util.BroadcastByType("main", "syncing", 0, Conf.Language(81), nil)
	err := bootSyncRepo()
	code := 1
	if err != nil {
		code = 2
	}
	util.BroadcastByType("main", "syncing", code, Conf.Sync.Stat, nil)
	return
}

func SyncData(byHand bool) {
	syncData(false, byHand)
}

func lockSync() {
	syncLock.Lock()
	isSyncing.Store(true)
}

func unlockSync() {
	isSyncing.Store(false)
	syncLock.Unlock()
}

func syncData(exit, byHand bool) {
	defer logging.Recover()

	if !checkSync(false, exit, byHand) {
		return
	}

	lockSync()
	defer unlockSync()

	util.BroadcastByType("main", "syncing", 0, Conf.Language(81), nil)
	if !exit && !isProviderOnline(byHand) { // 这个操作比较耗时，所以要先推送 syncing 事件后再判断网络，这样才能给用户更即时的反馈
		util.BroadcastByType("main", "syncing", 2, Conf.Language(28), nil)
		return
	}

	if exit {
		ExitSyncSucc = 0
		logging.LogInfof("sync before exit")
		msgId := util.PushMsg(Conf.Language(81), 1000*60*15)
		defer func() {
			util.PushClearMsg(msgId)
		}()
	}

	now := util.CurrentTimeMillis()
	Conf.Sync.Synced = now

	dataChanged, err := syncRepo(exit, byHand)
	code := 1
	if err != nil {
		code = 2
	}
	util.BroadcastByType("main", "syncing", code, Conf.Sync.Stat, nil)

	// Self-host fork: sync-perception WebSocket multi-device notification is removed.
	_ = dataChanged
	return
}

func checkSync(boot, exit, byHand bool) bool {
	if 2 == Conf.Sync.Mode && !boot && !exit && !byHand { // 手动模式下只有启动和退出进行同步
		return false
	}

	if 3 == Conf.Sync.Mode && !byHand { // 完全手动模式下只有手动进行同步
		return false
	}

	if !Conf.Sync.Enabled {
		if byHand {
			util.PushMsg(Conf.Language(124), 5000)
		}
		return false
	}

	if !cloud.IsValidCloudDirName(Conf.Sync.CloudName) {
		if byHand {
			util.PushMsg(Conf.Language(123), 5000)
		}
		return false
	}

	// Self-host fork: no subscription gating. If the user has configured a provider,
	// sync is allowed.

	if 7 < autoSyncErrCount && !byHand {
		logging.LogErrorf("failed to auto-sync too many times, delay auto-sync 64 minutes")
		util.PushErrMsg(Conf.Language(125), 1000*60*60)
		planSyncAfter(64 * time.Minute)
		return false
	}
	return true
}

// incReindex 增量重建索引。
func incReindex(upserts, removes []string) (upsertRootIDs, removeRootIDs []string) {
	upsertRootIDs = []string{}
	removeRootIDs = []string{}

	util.IncBootProgress(3, "Sync reindexing...")
	removeRootIDs = removeIndexes(removes) // 先执行 remove，否则移动文档时 upsert 会被忽略，导致未被索引
	upsertRootIDs = upsertIndexes(upserts)

	if 1 > len(removeRootIDs) {
		removeRootIDs = []string{}
	}
	if 1 > len(upsertRootIDs) {
		upsertRootIDs = []string{}
	}
	return
}

func removeIndexes(removeFilePaths []string) (removeRootIDs []string) {
	bootProgressPart := int32(10 / float64(len(removeFilePaths)))
	for _, removeFile := range removeFilePaths {
		if !strings.HasSuffix(removeFile, ".sy") {
			continue
		}

		rootID := util.GetTreeID(removeFile)
		removeRootIDs = append(removeRootIDs, rootID)

		msg := fmt.Sprintf(Conf.Language(39), rootID)
		util.IncBootProgress(bootProgressPart, msg)
		util.PushStatusBar(msg)

		cache.RemoveTreeData(rootID)
		sql.RemoveTreeQueue(rootID)
		bts := treenode.GetBlockTreesByRootID(rootID)
		for _, b := range bts {
			cache.RemoveBlockIAL(b.ID)
		}
		if block := treenode.GetBlockTree(rootID); nil != block {
			cache.RemoveDocIAL(block.Path)
		}
		treenode.RemoveBlockTreesByRootID(rootID)
	}

	if 1 > len(removeRootIDs) {
		removeRootIDs = []string{}
	}
	return
}

func upsertIndexes(upsertFilePaths []string) (upsertRootIDs []string) {
	luteEngine := util.NewLute()
	bootProgressPart := int32(10 / float64(len(upsertFilePaths)))
	for _, upsertFile := range upsertFilePaths {
		if !strings.HasSuffix(upsertFile, ".sy") {
			continue
		}

		upsertFile = filepath.ToSlash(upsertFile)
		upsertFile = strings.TrimPrefix(upsertFile, "/")

		box, _, found := strings.Cut(upsertFile, "/")
		if !found {
			// .sy 直接出现在 data 文件夹下，没有出现在笔记本文件夹下的情况
			continue
		}

		p := strings.TrimPrefix(upsertFile, box)
		msg := fmt.Sprintf(Conf.Language(40), util.GetTreeID(p))
		util.IncBootProgress(bootProgressPart, msg)
		util.PushStatusBar(msg)

		rootID := util.GetTreeID(p)
		cache.RemoveTreeData(rootID)
		tree, err0 := filesys.LoadTree(box, p, luteEngine)
		if nil != err0 {
			continue
		}
		treenode.UpsertBlockTree(tree)
		sql.UpsertTreeQueue(tree)

		bts := treenode.GetBlockTreesByRootID(rootID)
		for _, b := range bts {
			cache.RemoveBlockIAL(b.ID)
		}
		cache.RemoveDocIAL(tree.Path)

		upsertRootIDs = append(upsertRootIDs, rootID)
	}

	if 1 > len(upsertRootIDs) {
		upsertRootIDs = []string{}
	}
	return
}

func SetSyncGenerateConflictDoc(b bool) {
	Conf.Sync.GenerateConflictDoc = b
	Conf.Save()
}

func SetSyncEnable(b bool) {
	Conf.Sync.Enabled = b
	Conf.Save()
}

func SetSyncInterval(interval int) {
	if 30 > interval {
		interval = 30
	}
	if 43200 < interval {
		interval = 43200
	}

	Conf.Sync.Interval = interval
	Conf.Save()
	planSyncAfter(time.Duration(interval) * time.Second)
}

func SetSyncPerception(enabled bool) {
	if util.ContainerDocker == util.Container {
		enabled = false
	}

	Conf.Sync.Perception = enabled
	Conf.Save()

	if enabled {
		connectSyncWebSocket()
		return
	}

	closeSyncWebSocket()
}

func SetSyncMode(mode int) {
	Conf.Sync.Mode = mode
	Conf.Save()
}

func SetSyncProvider(provider int) (err error) {
	Conf.Sync.Provider = provider
	Conf.Save()
	return
}

func SetSyncProviderS3(s3 *conf.S3) (err error) {
	s3.Endpoint = strings.TrimSpace(s3.Endpoint)
	s3.Endpoint = util.NormalizeEndpoint(s3.Endpoint)
	s3.AccessKey = strings.TrimSpace(s3.AccessKey)
	s3.SecretKey = strings.TrimSpace(s3.SecretKey)
	s3.Bucket = strings.TrimSpace(s3.Bucket)
	s3.Region = strings.TrimSpace(s3.Region)
	s3.Timeout = util.NormalizeTimeout(s3.Timeout)
	s3.ConcurrentReqs = util.NormalizeConcurrentReqs(s3.ConcurrentReqs, conf.ProviderS3)

	if !cloud.IsValidCloudDirName(s3.Bucket) {
		util.PushErrMsg(Conf.Language(37), 5000)
		return
	}

	Conf.Sync.S3 = s3
	Conf.Save()
	return
}

func SetSyncProviderWebDAV(webdav *conf.WebDAV) (err error) {
	webdav.Endpoint = strings.TrimSpace(webdav.Endpoint)
	webdav.Endpoint = util.NormalizeEndpoint(webdav.Endpoint)

	// 不支持配置坚果云 WebDAV 进行同步 https://github.com/siyuan-note/siyuan/issues/7657
	if strings.Contains(strings.ToLower(webdav.Endpoint), "dav.jianguoyun.com") {
		err = errors.New(Conf.Language(194))
		return
	}

	webdav.Username = strings.TrimSpace(webdav.Username)
	webdav.Password = strings.TrimSpace(webdav.Password)
	webdav.Timeout = util.NormalizeTimeout(webdav.Timeout)
	webdav.ConcurrentReqs = util.NormalizeConcurrentReqs(webdav.ConcurrentReqs, conf.ProviderWebDAV)

	Conf.Sync.WebDAV = webdav
	Conf.Save()
	return
}

func SetSyncProviderLocal(local *conf.Local) (err error) {
	local.Endpoint = strings.TrimSpace(local.Endpoint)
	local.Endpoint = util.NormalizeLocalPath(local.Endpoint)

	absPath, err := filepath.Abs(local.Endpoint)
	if nil != err {
		msg := fmt.Sprintf("get endpoint [%s] abs path failed: %s", local.Endpoint, err)
		logging.LogErrorf(msg)
		err = fmt.Errorf(Conf.Language(77), msg)
		return
	}
	if !gulu.File.IsExist(absPath) {
		msg := fmt.Sprintf("endpoint [%s] not exist", local.Endpoint)
		logging.LogErrorf(msg)
		err = fmt.Errorf(Conf.Language(77), msg)
		return
	}
	if util.IsAbsPathInWorkspace(absPath) || filepath.Clean(absPath) == filepath.Clean(util.WorkspaceDir) {
		msg := fmt.Sprintf("endpoint [%s] is in workspace", local.Endpoint)
		logging.LogErrorf(msg)
		err = fmt.Errorf(Conf.Language(77), msg)
		return
	}

	if util.IsSubPath(absPath, util.WorkspaceDir) {
		msg := fmt.Sprintf("endpoint [%s] is parent of workspace", local.Endpoint)
		logging.LogErrorf(msg)
		err = fmt.Errorf(Conf.Language(77), msg)
		return
	}

	local.Timeout = util.NormalizeTimeout(local.Timeout)
	local.ConcurrentReqs = util.NormalizeConcurrentReqs(local.ConcurrentReqs, conf.ProviderLocal)

	Conf.Sync.Local = local
	Conf.Save()
	return
}

var (
	syncLock  = sync.Mutex{}
	isSyncing = atomic.Bool{}
)

// Self-host fork: CreateCloudSyncDir / RemoveCloudSyncDir / ListCloudSyncDir are removed.
// They only backed the b3log cloud directory picker UI. WebDAV / S3 / Local use their own
// explicit configuration screens and do not need a separate "cloud directory" concept.

func formatRepoErrorMsg(err error) string {
	msg := html.EscapeString(err.Error())
	if errors.Is(err, cloud.ErrCloudAuthFailed) {
		msg = Conf.Language(31)
	} else if errors.Is(err, cloud.ErrCloudObjectNotFound) {
		msg = Conf.Language(129)
	} else if errors.Is(err, dejavu.ErrLockCloudFailed) {
		msg = Conf.Language(188)
	} else if errors.Is(err, dejavu.ErrCloudLocked) {
		msg = Conf.Language(189)
	} else if errors.Is(err, dejavu.ErrRepoFatal) {
		msg = Conf.Language(23)
	} else if errors.Is(err, cloud.ErrSystemTimeIncorrect) {
		msg = Conf.Language(195)
	} else if errors.Is(err, cloud.ErrDeprecatedVersion) {
		msg = Conf.Language(212)
	} else if errors.Is(err, cloud.ErrCloudCheckFailed) {
		msg = Conf.Language(213)
	} else if errors.Is(err, cloud.ErrCloudServiceUnavailable) {
		msg = Conf.language(219)
	} else if errors.Is(err, cloud.ErrCloudForbidden) {
		msg = Conf.language(249)
	} else if errors.Is(err, cloud.ErrCloudTooManyRequests) {
		msg = Conf.language(250)
	} else if errors.Is(err, cloud.ErrDecryptFailed) {
		msg = Conf.Language(135)
	} else {
		logging.LogErrorf("sync failed caused by network: %s", msg)
		msgLowerCase := strings.ToLower(msg)
		if strings.Contains(msgLowerCase, "permission denied") || strings.Contains(msg, "access is denied") {
			msg = Conf.Language(33)
		} else if strings.Contains(msgLowerCase, "region was not a valid") {
			msg = Conf.language(254)
		} else if strings.Contains(msgLowerCase, "device or resource busy") || strings.Contains(msg, "is being used by another") {
			msg = fmt.Sprintf(Conf.Language(85), err)
		} else if strings.Contains(msgLowerCase, "cipher: message authentication failed") {
			msg = Conf.Language(135)
		} else if strings.Contains(msgLowerCase, "no such host") || strings.Contains(msgLowerCase, "connection failed") || strings.Contains(msgLowerCase, "hostname resolution") || strings.Contains(msgLowerCase, "No address associated with hostname") {
			msg = Conf.Language(24)
		} else if strings.Contains(msgLowerCase, "net/http: request canceled while waiting for connection") || strings.Contains(msgLowerCase, "exceeded while awaiting") || strings.Contains(msgLowerCase, "context deadline exceeded") || strings.Contains(msgLowerCase, "timeout") || strings.Contains(msgLowerCase, "context cancellation while reading body") {
			msg = Conf.Language(24)
		} else if strings.Contains(msgLowerCase, "connection") || strings.Contains(msgLowerCase, "refused") || strings.Contains(msgLowerCase, "socket") || strings.Contains(msgLowerCase, "eof") || strings.Contains(msgLowerCase, "closed") || strings.Contains(msgLowerCase, "network") {
			msg = Conf.Language(28)
		}
	}
	msg += " (Provider: " + conf.ProviderToStr(Conf.Sync.Provider) + ")"
	return msg
}

func getSyncIgnoreLines() (ret []string) {
	ignore := filepath.Join(util.DataDir, ".siyuan", "syncignore")
	err := os.MkdirAll(filepath.Dir(ignore), 0755)
	if err != nil {
		return
	}
	if !gulu.File.IsExist(ignore) {
		if err = gulu.File.WriteFileSafer(ignore, nil, 0644); err != nil {
			logging.LogErrorf("create syncignore [%s] failed: %s", ignore, err)
			return
		}
	}
	data, err := os.ReadFile(ignore)
	if err != nil {
		logging.LogErrorf("read syncignore [%s] failed: %s", ignore, err)
		return
	}
	dataStr := string(data)
	dataStr = strings.ReplaceAll(dataStr, "\r\n", "\n")
	ret = strings.Split(dataStr, "\n")

	// 忽略用户指南
	ret = append(ret, "20210808180117-6v0mkxr/**/*")
	ret = append(ret, "20210808180117-czj9bvb/**/*")
	ret = append(ret, "20211226090932-5lcq56f/**/*")
	ret = append(ret, "20240530133126-axarxgx/**/*")
	// 忽略用户指南的数据库 JSON 文件
	for _, avName := range getAllUserGuideAVJSONFiles() {
		ret = append(ret, "/storage/av/"+avName)
	}

	ret = gulu.Str.RemoveDuplicatedElem(ret)
	return
}

func IncSync() {
	syncSameCount.Store(0)
	planSyncAfter(time.Duration(Conf.Sync.Interval) * time.Second)
}

func planSyncAfter(d time.Duration) {
	syncPlanTimeLock.Lock()
	syncPlanTime = time.Now().Add(d)
	syncPlanTimeLock.Unlock()
}

func isProviderOnline(byHand bool) (ret bool) {
	var checkURL string
	skipTlsVerify := false
	switch Conf.Sync.Provider {
	case conf.ProviderS3:
		checkURL = Conf.Sync.S3.Endpoint
		skipTlsVerify = Conf.Sync.S3.SkipTlsVerify
	case conf.ProviderWebDAV:
		checkURL = Conf.Sync.WebDAV.Endpoint
		skipTlsVerify = Conf.Sync.WebDAV.SkipTlsVerify
	case conf.ProviderLocal:
		checkURL = "file://" + Conf.Sync.Local.Endpoint
	default:
		logging.LogWarnf("unknown provider: %d", Conf.Sync.Provider)
		return false
	}

	if ret = util.IsOnline(checkURL, skipTlsVerify, 7000); !ret {
		if 1 > autoSyncErrCount || byHand {
			util.PushErrMsg(Conf.Language(76)+" (Provider: "+conf.ProviderToStr(Conf.Sync.Provider)+")", 5000)
		}
		if !byHand {
			planSyncAfter(fixSyncInterval)
			autoSyncErrCount++
		}
	}
	return
}

// Self-host fork: the sync-perception WebSocket (b3log siyuan-sync.b3logfile.com) is
// removed. The OnlineKernel type is retained because the /api/sync/getSyncInfo endpoint
// still returns an empty kernels array to keep the frontend happy.

type OnlineKernel struct {
	ID       string `json:"id"`
	Hostname string `json:"hostname"`
	OS       string `json:"os"`
	Ver      string `json:"ver"`
}

func GetOnlineKernels() (ret []*OnlineKernel) {
	return []*OnlineKernel{}
}

func closeSyncWebSocket()   {}
func connectSyncWebSocket() {}

var KernelID = gulu.Rand.String(7)
