import {fetchPost, fetchSyncPost} from "../util/fetch";
import {showMessage} from "../dialog/message";
import {bindSyncCloudListEvent, getSyncCloudList} from "../sync/syncGuide";
import {processSync} from "../dialog/processSystem";
import {openByMobile} from "../protyle/util/compatibility";
import {confirmDialog} from "../dialog/confirmDialog";

const renderProvider = (provider: number) => {
    // Self-host fork: the upstream ProviderSiYuan (0) branch is gone. Existing
    // workspaces with provider=0 get migrated to ProviderLocal (4) at kernel
    // startup; we still guard against a stale value showing up here by falling
    // through to a neutral message.
    if (provider === 0) {
        return `<div class="b3-label b3-label--inner">
    Please choose a sync provider below (S3, WebDAV, or Local).
</div>`;
    }
    if (provider === 2) {
        return `<div class="b3-label b3-label--inner">
    ${window.siyuan.languages.syncThirdPartyProviderS3Intro}
    <div class="fn__hr"></div>
    ${window.siyuan.languages.syncThirdPartyProviderTip}
</div>
<div class="b3-label b3-label--inner fn__flex">
    <div class="fn__flex-center fn__size200">Endpoint</div>
    <div class="fn__space"></div>
    <input id="endpoint" class="b3-text-field fn__block">
</div>
<div class="b3-label b3-label--inner fn__flex">
    <div class="fn__flex-center fn__size200">Access Key</div>
    <div class="fn__space"></div>
    <input id="accessKey" class="b3-text-field fn__block">
</div>
<div class="b3-label b3-label--inner fn__flex">
    <div class="fn__flex-center fn__size200">Secret Key</div>
    <div class="fn__space"></div>
    <div class="b3-form__icona fn__block">
        <input id="secretKey" type="password" class="b3-text-field b3-form__icona-input">
        <svg class="b3-form__icona-icon" data-action="togglePassword"><use xlink:href="#iconEye"></use></svg>
    </div>
</div>
<div class="b3-label b3-label--inner fn__flex">
    <div class="fn__flex-center fn__size200">Bucket</div>
    <div class="fn__space"></div>
    <input id="bucket" class="b3-text-field fn__block">
</div>
<div class="b3-label b3-label--inner fn__flex">
    <div class="fn__flex-center fn__size200">Region ID</div>
    <div class="fn__space"></div>
    <input id="region" class="b3-text-field fn__block">
</div>
<div class="b3-label b3-label--inner fn__flex">
    <div class="fn__flex-center fn__size200">Timeout (s)</div>
    <div class="fn__space"></div>
    <input id="timeout" class="b3-text-field fn__block" type="number" min="7" max="300">
</div>
<div class="b3-label b3-label--inner fn__flex">
    <div class="fn__flex-center fn__size200">Addressing</div>
    <div class="fn__space"></div>
    <select class="b3-select fn__block" id="pathStyle">
        <option value="true">Path-style</option>
        <option value="false">Virtual-hosted-style</option>
    </select>
</div>
<div class="b3-label b3-label--inner fn__flex">
    <div class="fn__flex-center fn__size200">TLS Verify</div>
    <div class="fn__space"></div>
    <select class="b3-select fn__block" id="s3SkipTlsVerify">
        <option value="false">Verify</option>
        <option value="true">Skip</option>
    </select>
</div>
<div class="b3-label b3-label--inner fn__flex">
    <div class="fn__flex-center fn__size200">Concurrent Reqs</div>
    <div class="fn__space"></div>
    <input id="s3ConcurrentReqs" class="b3-text-field fn__block" type="number" min="1" max="16">
</div>
<div class="b3-label b3-label--inner fn__flex">
    <div class="fn__flex-1"></div>
    <button class="b3-button b3-button--outline fn__size200" data-action="purgeData">
        <svg><use xlink:href="#iconTrashcan"></use></svg>${window.siyuan.languages.cloudStoragePurge}
    </button>
    <div class="fn__space"></div>
    <button class="b3-button b3-button--outline fn__size200" style="position: relative">
        <input id="importData" class="b3-form__upload" type="file" data-type="s3">
        <svg><use xlink:href="#iconDownload"></use></svg>${window.siyuan.languages.import}
    </button>
    <div class="fn__space"></div>
    <button class="b3-button b3-button--outline fn__size200" data-action="exportData" data-type="s3">
        <svg><use xlink:href="#iconUpload"></use></svg>${window.siyuan.languages.export}
    </button>
</div>`;
    } else if (provider === 3) {
        return `<div class="b3-label b3-label--inner">
    ${window.siyuan.languages.syncThirdPartyProviderWebDAVIntro}
    <div class="fn__hr"></div>
    ${window.siyuan.languages.syncThirdPartyProviderTip}
</div>
<div class="b3-label b3-label--inner fn__flex">
    <div class="fn__flex-center fn__size200">Endpoint</div>
    <div class="fn__space"></div>
    <input id="endpoint" class="b3-text-field fn__block">
</div>
<div class="b3-label b3-label--inner fn__flex">
    <div class="fn__flex-center fn__size200">Username</div>
    <div class="fn__space"></div>
    <input id="username" class="b3-text-field fn__block">
</div>
<div class="b3-label b3-label--inner fn__flex">
    <div class="fn__flex-center fn__size200">Password</div>
    <div class="fn__space"></div>
    <div class="b3-form__icona fn__block">
        <input id="password" type="password" class="b3-text-field b3-form__icona-input">
        <svg class="b3-form__icona-icon" data-action="togglePassword"><use xlink:href="#iconEye"></use></svg>
    </div>
</div>
<div class="b3-label b3-label--inner fn__flex">
    <div class="fn__flex-center fn__size200">Timeout (s)</div>
    <div class="fn__space"></div>
    <input id="timeout" class="b3-text-field fn__block" type="number" min="7" max="300">
</div>
<div class="b3-label b3-label--inner fn__flex">
    <div class="fn__flex-center fn__size200">TLS Verify</div>
    <div class="fn__space"></div>
    <select class="b3-select fn__block" id="webdavSkipTlsVerify">
        <option value="false">Verify</option>
        <option value="true">Skip</option>
    </select>
</div>
<div class="b3-label b3-label--inner fn__flex">
    <div class="fn__flex-center fn__size200">Concurrent Reqs</div>
    <div class="fn__space"></div>
    <input id="webdavConcurrentReqs" class="b3-text-field fn__block" type="number" min="1" max="16">
</div>
<div class="b3-label b3-label--inner fn__flex">
    <div class="fn__flex-1"></div>
    <button class="b3-button b3-button--outline fn__size200" data-action="purgeData">
        <svg><use xlink:href="#iconTrashcan"></use></svg>${window.siyuan.languages.cloudStoragePurge}
    </button>
    <div class="fn__space"></div>
    <button class="b3-button b3-button--outline fn__size200" style="position: relative">
        <input id="importData" class="b3-form__upload" type="file" data-type="webdav">
        <svg><use xlink:href="#iconDownload"></use></svg>${window.siyuan.languages.import}
    </button>
    <div class="fn__space"></div>
    <button class="b3-button b3-button--outline fn__size200" data-action="exportData" data-type="webdav">
        <svg><use xlink:href="#iconUpload"></use></svg>${window.siyuan.languages.export}
    </button>
</div>`;
    } else if (provider === 4) {
        return `<div class="b3-label b3-label--inner">
    <div class="ft__error">
        ${window.siyuan.languages.mobileNotSupport}
    </div>
    <div class="fn__hr"></div>
    ${window.siyuan.languages.syncThirdPartyProviderLocalIntro}
</div>
<div class="b3-label b3-label--inner fn__flex">
    <div class="fn__flex-center fn__size200">Endpoint</div>
    <div class="fn__space"></div>
    <input id="endpoint" class="b3-text-field fn__block">
</div>
<div class="b3-label b3-label--inner fn__flex">
    <div class="fn__flex-center fn__size200">Timeout (s)</div>
    <div class="fn__space"></div>
    <input id="timeout" class="b3-text-field fn__block" type="number" min="7" max="300">
</div>
<div class="b3-label b3-label--inner fn__flex">
    <div class="fn__flex-center fn__size200">Concurrent Reqs</div>
    <div class="fn__space"></div>
    <input id="localConcurrentReqs" class="b3-text-field fn__block" type="number" min="1" max="1024">
</div>`;
    }
    return "";
};

const fillSyncProviderPanelValues = (panel: Element) => {
    const provider = window.siyuan.config.sync.provider;
    if (provider === 2) {
        const s3 = window.siyuan.config.sync.s3;
        (panel.querySelector("#endpoint") as HTMLInputElement).value = String(s3.endpoint);
        (panel.querySelector("#accessKey") as HTMLInputElement).value = String(s3.accessKey);
        (panel.querySelector("#secretKey") as HTMLInputElement).value = String(s3.secretKey);
        (panel.querySelector("#bucket") as HTMLInputElement).value = String(s3.bucket);
        (panel.querySelector("#region") as HTMLInputElement).value = String(s3.region);
        (panel.querySelector("#timeout") as HTMLInputElement).value = String(s3.timeout);
        (panel.querySelector("#pathStyle") as HTMLSelectElement).value = s3.pathStyle ? "true" : "false";
        (panel.querySelector("#s3SkipTlsVerify") as HTMLSelectElement).value = s3.skipTlsVerify ? "true" : "false";
        (panel.querySelector("#s3ConcurrentReqs") as HTMLInputElement).value = String(s3.concurrentReqs);
    } else if (provider === 3) {
        const webdav = window.siyuan.config.sync.webdav;
        (panel.querySelector("#endpoint") as HTMLInputElement).value = String(webdav.endpoint);
        (panel.querySelector("#username") as HTMLInputElement).value = String(webdav.username);
        (panel.querySelector("#password") as HTMLInputElement).value = String(webdav.password);
        (panel.querySelector("#timeout") as HTMLInputElement).value = String(webdav.timeout);
        (panel.querySelector("#webdavSkipTlsVerify") as HTMLSelectElement).value = webdav.skipTlsVerify ? "true" : "false";
        (panel.querySelector("#webdavConcurrentReqs") as HTMLInputElement).value = String(webdav.concurrentReqs);
    } else if (provider === 4) {
        const local = window.siyuan.config.sync.local;
        (panel.querySelector("#endpoint") as HTMLInputElement).value = String(local.endpoint);
        (panel.querySelector("#timeout") as HTMLInputElement).value = String(local.timeout);
        (panel.querySelector("#localConcurrentReqs") as HTMLInputElement).value = String(local.concurrentReqs);
    }
};

const bindProviderEvent = () => {
    const importElement = repos.element.querySelector("#importData") as HTMLInputElement;
    if (importElement) {
        importElement.addEventListener("change", () => {
            const formData = new FormData();
            formData.append("file", importElement.files[0]);
            const isS3 = importElement.getAttribute("data-type") === "s3";
            fetchPost(isS3 ? "/api/sync/importSyncProviderS3" : "/api/sync/importSyncProviderWebDAV", formData, (response) => {
                if (isS3) {
                    window.siyuan.config.sync.s3 = response.data.s3;
                } else {
                    window.siyuan.config.sync.webdav = response.data.webdav;
                }
                repos.element.querySelector("#syncProviderPanel").innerHTML = renderProvider(window.siyuan.config.sync.provider);
                bindProviderEvent();
                showMessage(window.siyuan.languages.imported);
                importElement.value = "";
            });
        });
    }

    const reposDataElement = repos.element.querySelector("#reposData");
    const loadingElement = repos.element.querySelector("#reposLoading");
    // Self-host fork: no cloud-space panel (no b3log account, no quota to display).
    // The #reposData panel stays hidden; WebDAV/S3/Local provider settings live below.
    loadingElement.classList.add("fn__none");
    let nextElement = reposDataElement.nextElementSibling;
    while (nextElement) {
        nextElement.classList.remove("fn__none");
        nextElement = nextElement.nextElementSibling;
    }
    reposDataElement.classList.add("fn__none");
    const providerPanelElement = repos.element.querySelector("#syncProviderPanel");
    fillSyncProviderPanelValues(providerPanelElement);
    providerPanelElement.querySelectorAll(".b3-text-field, .b3-select").forEach(item => {
        item.addEventListener("blur", () => {
            if (window.siyuan.config.sync.provider === 2) {
                const s3TimeoutInput = providerPanelElement.querySelector("#timeout") as HTMLInputElement;
                let timeout = parseInt(s3TimeoutInput.value, 10);
                if (7 > timeout) {
                    if (1 > timeout) {
                        timeout = 30;
                    } else {
                        timeout = 7;
                    }
                }
                if (300 < timeout) {
                    timeout = 300;
                }
                let concurrentReqs = parseInt((providerPanelElement.querySelector("#s3ConcurrentReqs") as HTMLInputElement).value, 10);
                if (1 > concurrentReqs) {
                    concurrentReqs = 1;
                }
                if (16 < concurrentReqs) {
                    concurrentReqs = 16;
                }
                s3TimeoutInput.value = timeout.toString();
                const s3 = {
                    endpoint: (providerPanelElement.querySelector("#endpoint") as HTMLInputElement).value,
                    accessKey: (providerPanelElement.querySelector("#accessKey") as HTMLInputElement).value.trim(),
                    secretKey: (providerPanelElement.querySelector("#secretKey") as HTMLInputElement).value.trim(),
                    bucket: (providerPanelElement.querySelector("#bucket") as HTMLInputElement).value.trim(),
                    pathStyle: (providerPanelElement.querySelector("#pathStyle") as HTMLSelectElement).value === "true",
                    region: (providerPanelElement.querySelector("#region") as HTMLInputElement).value.trim(),
                    skipTlsVerify: (providerPanelElement.querySelector("#s3SkipTlsVerify") as HTMLSelectElement).value === "true",
                    timeout: timeout,
                    concurrentReqs: concurrentReqs,
                };
                // 使用 fetchSyncPost：内核返回 code < 0 时 fetchPost 不会调用回调，此处需始终回写界面与已保存配置一致
                fetchSyncPost("/api/sync/setSyncProviderS3", {s3})
                    .then((response) => {
                        if (response.code === 0 && response.data?.s3) {
                            window.siyuan.config.sync.s3 = response.data.s3;
                        }
                    })
                    .finally(() => {
                        fillSyncProviderPanelValues(providerPanelElement);
                    })
                    .catch(() => {});
            } else if (window.siyuan.config.sync.provider === 3) {
                const webdavTimeoutInput = providerPanelElement.querySelector("#timeout") as HTMLInputElement;
                let timeout = parseInt(webdavTimeoutInput.value, 10);
                if (7 > timeout) {
                    timeout = 7;
                }
                if (300 < timeout) {
                    timeout = 300;
                }
                let concurrentReqs = parseInt((providerPanelElement.querySelector("#webdavConcurrentReqs") as HTMLInputElement).value, 10);
                if (1 > concurrentReqs) {
                    concurrentReqs = 1;
                }
                if (16 < concurrentReqs) {
                    concurrentReqs = 16;
                }
                webdavTimeoutInput.value = timeout.toString();
                const webdav = {
                    endpoint: (providerPanelElement.querySelector("#endpoint") as HTMLInputElement).value,
                    username: (providerPanelElement.querySelector("#username") as HTMLInputElement).value.trim(),
                    password: (providerPanelElement.querySelector("#password") as HTMLInputElement).value.trim(),
                    skipTlsVerify: (providerPanelElement.querySelector("#webdavSkipTlsVerify") as HTMLSelectElement).value === "true",
                    timeout: timeout,
                    concurrentReqs: concurrentReqs,
                };
                fetchSyncPost("/api/sync/setSyncProviderWebDAV", {webdav})
                    .then((response) => {
                        if (response.code === 0 && response.data?.webdav) {
                            window.siyuan.config.sync.webdav = response.data.webdav;
                        }
                    })
                    .finally(() => {
                        fillSyncProviderPanelValues(providerPanelElement);
                    })
                    .catch(() => {});
            } else if (window.siyuan.config.sync.provider === 4) {
                const localTimeoutInput = providerPanelElement.querySelector("#timeout") as HTMLInputElement;
                let timeout = parseInt(localTimeoutInput.value, 10);
                if (7 > timeout) {
                    timeout = 7;
                }
                if (300 < timeout) {
                    timeout = 300;
                }
                let concurrentReqs = parseInt((providerPanelElement.querySelector("#localConcurrentReqs") as HTMLInputElement).value, 10);
                if (1 > concurrentReqs) {
                    concurrentReqs = 1;
                }
                if (1024 < concurrentReqs) {
                    concurrentReqs = 1024;
                }
                localTimeoutInput.value = timeout.toString();
                const local = {
                    endpoint: (providerPanelElement.querySelector("#endpoint") as HTMLInputElement).value,
                    timeout: timeout,
                    concurrentReqs: concurrentReqs,
                };
                fetchSyncPost("/api/sync/setSyncProviderLocal", {local})
                    .then((response) => {
                        if (response.code === 0 && response.data?.local) {
                            window.siyuan.config.sync.local = response.data.local;
                        }
                    })
                    .finally(() => {
                        fillSyncProviderPanelValues(providerPanelElement);
                    })
                    .catch(() => {});
            }
        });
    });
};

export const repos = {
    element: undefined as Element,
    genHTML: () => {
        return `<div>
<div style="position: fixed;width: 800px;height: 434px;box-sizing: border-box;text-align: center;display: flex;align-items: center;justify-content: center;z-index: 1;" id="reposLoading">
    <img src="/stage/loading-pure.svg">
</div>
<div class="fn__flex b3-label config__item">
    <div class="fn__flex-1">
        ${window.siyuan.languages.syncProvider}
        <div class="b3-label__text">${window.siyuan.languages.syncProviderTip}</div>
    </div>
    <span class="fn__space"></span>
    <select id="syncProvider" class="b3-select fn__flex-center fn__size200">
        <option value="2" ${window.siyuan.config.sync.provider === 2 ? "selected" : ""}>S3</option>
        <option value="3" ${window.siyuan.config.sync.provider === 3 ? "selected" : ""}>WebDAV</option>
        <option class="${!["std", "docker"].includes(window.siyuan.config.system.container) ? "fn__none" : ""}" value="4" ${window.siyuan.config.sync.provider === 4 ? "selected" : ""}>${window.siyuan.languages.localFileSystem}</option>
    </select>
</div>
<div id="syncProviderPanel" class="b3-label">
    ${renderProvider(window.siyuan.config.sync.provider)}
</div>
<div id="reposData" class="b3-label">
    <div class="fn__flex">
        <div class="fn__flex-1">
            ${window.siyuan.languages.cloudStorage}
        </div>
        <div class="fn__flex-1">
            ${window.siyuan.languages.trafficStat}
        </div>
    </div>
</div>
<label class="fn__flex b3-label">
    <div class="fn__flex-1">
        ${window.siyuan.languages.openSyncTip1}
        <div class="b3-label__text">${window.siyuan.languages.openSyncTip2}</div>
    </div>
    <span class="fn__space"></span>
    <input type="checkbox" id="reposCloudSyncSwitch"${window.siyuan.config.sync.enabled ? " checked='checked'" : ""} class="b3-switch fn__flex-center">
</label>
<label class="fn__flex b3-label">
    <div class="fn__flex-1">
        ${window.siyuan.languages.generateConflictDoc}
        <div class="b3-label__text">${window.siyuan.languages.generateConflictDocTip}</div>
    </div>
    <span class="fn__space"></span>
    <input type="checkbox" id="generateConflictDoc"${window.siyuan.config.sync.generateConflictDoc ? " checked='checked'" : ""} class="b3-switch fn__flex-center">
</label>
<div class="b3-label">
    <div class="fn__flex config__item">
        <div class="fn__flex-1">
            ${window.siyuan.languages.syncMode}
            <div class="b3-label__text">${window.siyuan.languages.syncModeTip}</div>
        </div>
        <span class="fn__space"></span>
        <select id="syncMode" class="b3-select fn__flex-center fn__size200">
            <option value="1" ${window.siyuan.config.sync.mode === 1 ? "selected" : ""}>${window.siyuan.languages.syncMode1}</option>
            <option value="2" ${window.siyuan.config.sync.mode === 2 ? "selected" : ""}>${window.siyuan.languages.syncMode2}</option>
            <option value="3" ${window.siyuan.config.sync.mode === 3 ? "selected" : ""}>${window.siyuan.languages.syncMode3}</option>
        </select>
    </div>
    <div class="fn__flex b3-label${(window.siyuan.config.sync.mode !== 1) ? " fn__none" : ""}">
        <div class="fn__flex-1">
            ${window.siyuan.languages.syncInterval}
            <div class="b3-label__text">${window.siyuan.languages.syncIntervalTip}</div>
        </div>
        <span class="fn__space"></span>
        <input type="number" min="30" max="43200" id="syncInterval" class="b3-text-field fn__flex-center" value="${window.siyuan.config.sync.interval}" >
        <span class="fn__space"></span>        
        <span class="fn__flex-center ft__on-surface">${window.siyuan.languages.second}</span> 
    </div>
    <label class="fn__flex b3-label${(window.siyuan.config.sync.mode !== 1 || window.siyuan.config.system.container === "docker" || window.siyuan.config.sync.provider !== 0) ? " fn__none" : ""}">
        <div class="fn__flex-1">
            ${window.siyuan.languages.syncPerception}
            <div class="b3-label__text">${window.siyuan.languages.syncPerceptionTip}</div>
        </div>
        <span class="fn__space"></span>
        <input type="checkbox" id="syncPerception"${window.siyuan.config.sync.perception ? " checked='checked'" : ""} class="b3-switch fn__flex-center">
    </label>
</div>
<div class="b3-label">
    <div class="fn__flex config__item">
        <div class="fn__flex-1">
            ${window.siyuan.languages.cloudSyncDir}
            <div class="b3-label__text">${window.siyuan.languages.cloudSyncDirTip}</div>
        </div>
        <div class="fn__space"></div>
        <button class="b3-button b3-button--outline fn__flex-center fn__size200" data-action="config">
            <svg><use xlink:href="#iconSettings"></use></svg>${window.siyuan.languages.config}
        </button>
    </div>
    <div id="reposCloudSyncList" class="fn__none b3-label"><img style="margin: 0 auto;display: block;width: 64px;height: 100%" src="/stage/loading-pure.svg"></div>
</div>
<div class="b3-label fn__flex">
    <div class="fn__flex-center">${window.siyuan.languages.cloudBackup}</div>
    <div class="b3-list-item__meta fn__flex-center">${window.siyuan.languages.cloudBackupTip}</div>
</div>
</div>`;
    },
    bindEvent: () => {
        bindProviderEvent();
        const switchElement = repos.element.querySelector("#reposCloudSyncSwitch") as HTMLInputElement;
        switchElement.addEventListener("change", () => {
            if (switchElement.checked && window.siyuan.config.sync.cloudName === "") {
                switchElement.checked = false;
                showMessage(window.siyuan.languages._kernel[123]);
                return;
            }
            fetchPost("/api/sync/setSyncEnable", {enabled: switchElement.checked}, () => {
                window.siyuan.config.sync.enabled = switchElement.checked;
                processSync();
            });
        });
        const syncIntervalElement = repos.element.querySelector("#syncInterval") as HTMLInputElement;
        syncIntervalElement.addEventListener("change", () => {
            let interval = parseInt(syncIntervalElement.value);
            if (30 > interval) {
                interval = 30;
            }
            if (43200 < interval) {
                interval = 43200;
            }
            syncIntervalElement.value = interval.toString();
            fetchPost("/api/sync/setSyncInterval", {interval: interval}, () => {
                window.siyuan.config.sync.interval = interval;
                processSync();
            });
        });
        const syncPerceptionElement = repos.element.querySelector("#syncPerception") as HTMLInputElement;
        syncPerceptionElement.addEventListener("change", () => {
            fetchPost("/api/sync/setSyncPerception", {enabled: syncPerceptionElement.checked}, () => {
                window.siyuan.config.sync.perception = syncPerceptionElement.checked;
                processSync();
            });
        });
        const switchConflictElement = repos.element.querySelector("#generateConflictDoc") as HTMLInputElement;
        switchConflictElement.addEventListener("change", () => {
            fetchPost("/api/sync/setSyncGenerateConflictDoc", {enabled: switchConflictElement.checked}, () => {
                window.siyuan.config.sync.generateConflictDoc = switchConflictElement.checked;
            });
        });
        const syncModeElement = repos.element.querySelector("#syncMode") as HTMLSelectElement;
        syncModeElement.addEventListener("change", () => {
            fetchPost("/api/sync/setSyncMode", {mode: parseInt(syncModeElement.value, 10)}, () => {
                // Self-host fork: sync perception (b3log multi-device push) is gone,
                // so its toggle stays hidden regardless of mode/provider.
                syncPerceptionElement.parentElement.classList.add("fn__none");
                if (syncModeElement.value === "1") {
                    syncIntervalElement.parentElement.classList.remove("fn__none");
                } else {
                    syncIntervalElement.parentElement.classList.add("fn__none");
                }
                window.siyuan.config.sync.mode = parseInt(syncModeElement.value, 10);
            });
        });
        const syncConfigElement = repos.element.querySelector("#reposCloudSyncList");
        const syncProviderElement = repos.element.querySelector("#syncProvider") as HTMLSelectElement;
        syncProviderElement.addEventListener("change", () => {
            fetchPost("/api/sync/setSyncProvider", {provider: parseInt(syncProviderElement.value, 10)}, (response) => {
                if (response.code === 1) {
                    showMessage(response.msg);
                    syncProviderElement.value = "0";
                    window.siyuan.config.sync.provider = 0;
                } else {
                    window.siyuan.config.sync.provider = parseInt(syncProviderElement.value, 10);
                }
                repos.element.querySelector("#syncProviderPanel").innerHTML = renderProvider(window.siyuan.config.sync.provider);
                bindProviderEvent();
                syncConfigElement.innerHTML = "";
                syncConfigElement.classList.add("fn__none");
                if (window.siyuan.config.sync.mode !== 1 || window.siyuan.config.system.container === "docker" || window.siyuan.config.sync.provider !== 0) {
                    syncPerceptionElement.parentElement.classList.add("fn__none");
                } else {
                    syncPerceptionElement.parentElement.classList.remove("fn__none");
                }
                if (window.siyuan.config.sync.mode !== 1) {
                    syncIntervalElement.parentElement.classList.add("fn__none");
                } else {
                    syncIntervalElement.parentElement.classList.remove("fn__none");
                }
            });
        });
        const loadingElement = repos.element.querySelector("#reposLoading") as HTMLElement;
        loadingElement.style.width = repos.element.clientWidth + "px";
        loadingElement.style.height = repos.element.clientHeight + "px";
        bindSyncCloudListEvent(syncConfigElement);
        repos.element.firstElementChild.addEventListener("click", (event) => {
            let target = event.target as HTMLElement;
            while (target && target !== repos.element) {
                const action = target.getAttribute("data-action");
                if (action === "config") {
                    if (syncConfigElement.classList.contains("fn__none")) {
                        getSyncCloudList(syncConfigElement, true);
                        syncConfigElement.classList.remove("fn__none");
                    } else {
                        syncConfigElement.classList.add("fn__none");
                    }
                    break;
                } else if (action === "togglePassword") {
                    const isEye = target.firstElementChild.getAttribute("xlink:href") === "#iconEye";
                    target.firstElementChild.setAttribute("xlink:href", isEye ? "#iconEyeoff" : "#iconEye");
                    target.previousElementSibling.setAttribute("type", isEye ? "text" : "password");
                    break;
                } else if (action === "exportData") {
                    fetchPost(target.getAttribute("data-type") === "s3" ? "/api/sync/exportSyncProviderS3" : "/api/sync/exportSyncProviderWebDAV", {}, response => {
                        openByMobile(response.data.zip);
                    });
                    break;
                } else if (action === "purgeData") {
                    confirmDialog("♻️ " + window.siyuan.languages.cloudStoragePurge, `<div class="b3-typography">${window.siyuan.languages.cloudStoragePurgeConfirm}</div>`, () => {
                        fetchPost("/api/repo/purgeCloudRepo");
                    });
                    break;
                }
                target = target.parentElement;
            }
        });
    },
};
