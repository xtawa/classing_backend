const state = {
  session: readSession(),
  account: null,
  view: "overview",
  authMode: "login",
  refreshing: null,
  registrationChallengeId: "",
  registrationConfig: null,
  turnstileWidgetId: null,
  settingsStreamAbort: null,
  settingsStreamRetry: null,
  cloudEventVersion: 0,
  settingsFormDirty: false,
  settingsDirtyFields: new Set(),
  settingsRendering: false,
};

const authShell = document.getElementById("authShell");
const prototype = document.getElementById("prototype");
const content = document.getElementById("content");
const navItems = Array.from(document.querySelectorAll(".nav-item"));
const tweaks = document.getElementById("tweaks");
const tweaksTrigger = document.getElementById("tweaksTrigger");

const viewCopy = {
  overview: ["运营总览", "OVERVIEW", "一张工作台，看清系统状态", "账户、课表、会员、同步与投递链路会在这里汇总。"],
  users: ["用户与权限", "IDENTITY", "管理账户与访问边界", "查询用户、调整角色，并及时停用风险账户。"],
  schedules: ["课表项目", "TIMETABLES", "每一份课表都有清晰归属", "管理学期、时区、同步版本和课程数据。"],
  membership: ["会员订阅", "MEMBERSHIP", "查看权益，或使用兑换码升级", "会员将解锁官方云同步与更多服务能力。"],
  askAi: ["Ask AI", "TIMETABLE AI", "向你的当前课表提问", "新对话会安全地附带选中的课表；历史会话可在所有客户端继续。"],
  redeem: ["兑换码", "REDEMPTION", "以批次管理权益发放", "创建唯一兑换码或活动码，并追踪额度与核销状态。"],
  briefings: ["每日简报", "BRIEFING", "在正确时间送达下一天课程", "选择邮箱渠道、投递时间和时区，并随时发送测试任务。"],
  releases: ["公告与版本", "RELEASES", "从一个入口发布公告与安装包", "上传 APK、发布稳定版本，并控制客户端可见的公告。"],
  mail: ["邮件与任务", "DELIVERY", "观察邮箱池与投递队列", "管理员可以配置 SMTP Secret 引用并重试失败任务。"],
  audit: ["审计日志", "AUDIT", "关键操作都有可检索轨迹", "账户、权益、同步与系统设置变化都会留下记录。"],
  aiAdmin: ["AI 管理", "AI OPERATIONS", "管理模型、提示词与会员资源限额", "仅展示脱敏配置和用量元数据，不读取用户聊天正文。"],
  settings: ["账户设置", "SETTINGS", "管理个人资料与账户安全", "修改用户名、邮箱和密码；管理员还能调整安全的运行时设置。"],
};

function readSession() {
  try { return JSON.parse(sessionStorage.getItem("classing.session") || "null"); } catch { return null; }
}

function saveSession(session) {
  state.session = session;
  if (session) sessionStorage.setItem("classing.session", JSON.stringify(session));
  else sessionStorage.removeItem("classing.session");
}

function cloudCursorKey() { return `classing.cloud.cursor.${state.account?.userId || "anonymous"}`; }

function loadCloudCursor() {
  const value = Number(localStorage.getItem(cloudCursorKey()) || 0);
  return Number.isSafeInteger(value) && value >= 0 ? value : 0;
}

function saveCloudCursor(version) {
  if (!Number.isSafeInteger(version) || version < state.cloudEventVersion) return;
  state.cloudEventVersion = version;
  localStorage.setItem(cloudCursorKey(), String(version));
}

function browserDeviceId() {
  const key = "classing.cloud.deviceId";
  let value = localStorage.getItem(key);
  if (!value) {
    value = `web-${crypto.randomUUID()}`;
    localStorage.setItem(key, value);
  }
  return value;
}

function sleep(milliseconds) { return new Promise((resolve) => setTimeout(resolve, milliseconds)); }

function authConsentPayload() {
  return {
    privacyPolicy: true,
    termsOfService: true,
    crossBorderTransfer: true,
    acceptedAt: Date.now(),
    client: "web",
  };
}

function legalAgreementUrls() {
  return state.registrationConfig?.legalAgreementUrls || {};
}

function legalAgreementUrlsReady() {
  const urls = legalAgreementUrls();
  return Boolean(urls.privacyPolicy && urls.termsOfService && urls.crossBorderTransfer);
}

function updateAuthLegalLinks() {
  const urls = legalAgreementUrls();
  [
    ["privacyPolicyLink", urls.privacyPolicy],
    ["termsOfServiceLink", urls.termsOfService],
    ["crossBorderTransferLink", urls.crossBorderTransfer],
  ].forEach(([id, url]) => {
    const item = document.getElementById(id);
    if (!item) return;
    item.href = url || "#";
    item.setAttribute("aria-disabled", url ? "false" : "true");
  });
  updateAuthSubmitState();
}

function updateAuthSubmitState() {
  const consent = document.getElementById("authConsent");
  const submit = document.getElementById("authSubmit");
  if (!consent || !submit) return;
  submit.disabled = !consent.checked || !legalAgreementUrlsReady();
}

async function ensureRegistrationConfig() {
  if (state.registrationConfig) return state.registrationConfig;
  state.registrationConfig = await (await safeFetch("/api/v1/auth/registration/config")).json();
  updateAuthLegalLinks();
  return state.registrationConfig;
}

async function safeFetch(path, options) {
  try {
    return await fetch(path, options);
  } catch (error) {
    if (error instanceof TypeError) {
      throw new Error("无法连接到后端，请检查当前控制台域名、网络连接或浏览器跨域限制");
    }
    throw error;
  }
}

async function runButtonAction(button, action) {
  if (!button || button.disabled) return;
  button.disabled = true;
  try {
    await action();
  } finally {
    button.disabled = false;
  }
}

async function api(path, options = {}, retry = true) {
  const headers = new Headers(options.headers || {});
  if (options.body && !(options.body instanceof FormData) && !headers.has("Content-Type")) headers.set("Content-Type", "application/json");
  if (state.session?.accessToken) headers.set("Authorization", `Bearer ${state.session.accessToken}`);
  const response = await safeFetch(path, { ...options, headers });
  if (response.status === 401 && retry && state.session?.refreshToken && !path.endsWith("/auth/refresh")) {
    if (await refreshSession()) return api(path, options, false);
  }
  const text = await response.text();
  let body = null;
  if (text) { try { body = JSON.parse(text); } catch { body = { message: text }; } }
  if (!response.ok) {
    const error = new Error(body?.message || `HTTP ${response.status}`);
    error.status = response.status;
    error.code = body?.code;
    throw error;
  }
  return body;
}

async function refreshSession() {
  if (state.refreshing) return state.refreshing;
  state.refreshing = (async () => {
    try {
      const response = await safeFetch("/api/v1/auth/refresh", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ refreshToken: state.session.refreshToken }) });
      if (!response.ok) throw new Error("refresh failed");
      const body = await response.json();
      saveSession(body.session);
      return true;
    } catch {
      signOut(false);
      return false;
    } finally { state.refreshing = null; }
  })();
  return state.refreshing;
}

function escapeHTML(value) {
  return String(value ?? "").replace(/[&<>'"]/g, (char) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", "'": "&#39;", '"': "&quot;" }[char]));
}

function formatDate(value, withTime = false) {
  if (!value) return "—";
  return new Intl.DateTimeFormat("zh-CN", withTime ? { dateStyle: "medium", timeStyle: "short" } : { dateStyle: "medium" }).format(new Date(Number(value)));
}

function toast(message, type = "success") {
  const item = document.createElement("div");
  item.className = `toast ${type === "error" ? "error" : ""}`;
  item.textContent = message;
  document.getElementById("toastRegion").appendChild(item);
  setTimeout(() => item.remove(), 4200);
}

function setLoading() { content.innerHTML = `<div class="loading-state">正在读取服务数据…</div>`; }

function hero(view, extra = "") {
  const copy = viewCopy[view] || viewCopy.overview;
  return `<div class="runtime-hero"><div><p class="eyebrow">${copy[1]}</p><h1>${copy[2]}</h1><p>${copy[3]}</p></div>${extra}</div>`;
}

function emptyState(title, description) {
  return `<div class="empty-state"><strong>${escapeHTML(title)}</strong><span>${escapeHTML(description)}</span></div>`;
}

function isAdmin() { return state.account?.role === "ADMIN"; }

async function boot() {
  bindChrome();
  if (!state.session) { showAuth(); return; }
  try {
    const response = await api("/api/v1/account/me");
    state.account = response.account;
    showConsole();
  } catch { signOut(false); }
}

function showAuth() {
  prototype.hidden = true;
  authShell.hidden = false;
  setAuthMode("login");
  ensureRegistrationConfig().catch(() => {
    document.getElementById("authError").textContent = "协议链接配置无法加载，请稍后重试";
    updateAuthSubmitState();
  });
  document.getElementById("authIdentifier").focus();
}

function showConsole() {
  authShell.hidden = true;
  prototype.hidden = false;
  document.querySelector(".service-state").innerHTML = `<span class="connected"></span>服务已连接`;
  syncAccountChrome();
  state.cloudEventVersion = loadCloudCursor();
  startSettingsStream();
  setView(isAdmin() ? "overview" : "schedules");
}

function syncAccountChrome() {
  document.querySelectorAll("[data-admin='true']").forEach((item) => { item.hidden = !isAdmin(); });
  const avatar = document.querySelector("#accountButton .avatar");
  avatar.textContent = (state.account.username || state.account.email || "U").slice(0, 1).toUpperCase();
  document.getElementById("accountChipName").textContent = state.account.username;
  document.getElementById("accountChipRole").textContent = isAdmin() ? "管理员" : "普通用户";
}

function bindChrome() {
  navItems.forEach((item) => item.addEventListener("click", () => setView(item.dataset.view)));
  document.getElementById("accountButton").addEventListener("click", () => setView("settings"));
  document.getElementById("logoutButton").addEventListener("click", () => signOut(true));
  document.getElementById("mobileMenu").addEventListener("click", () => document.body.classList.toggle("nav-open"));
  document.getElementById("scrim").addEventListener("click", () => document.body.classList.remove("nav-open"));
  tweaksTrigger.addEventListener("click", () => setTweaks(true));
  document.getElementById("closeTweaks").addEventListener("click", () => setTweaks(false));
  document.getElementById("schemeSelect").addEventListener("change", (event) => {
    document.body.classList.remove("scheme-mint", "scheme-sunrise");
    if (event.target.value !== "classing") document.body.classList.add(`scheme-${event.target.value}`);
  });
  document.getElementById("densitySelect").addEventListener("change", (event) => {
    document.body.classList.remove("compact", "focus");
    if (event.target.value !== "comfortable") document.body.classList.add(event.target.value);
  });
  document.getElementById("themeToggle").addEventListener("change", (event) => document.body.classList.toggle("dark", event.target.checked));
  document.getElementById("authSwitch").addEventListener("click", () => setAuthMode(state.authMode === "login" ? "register" : "login"));
  document.getElementById("authConsent").addEventListener("change", updateAuthSubmitState);
  document.getElementById("authForm").addEventListener("submit", submitAuth);
  document.getElementById("resetLink").addEventListener("click", requestReset);
  document.getElementById("materialFab").addEventListener("click", handleFab);
}

function setTweaks(open) {
  tweaks.classList.toggle("open", open);
  tweaksTrigger.hidden = open;
  tweaksTrigger.setAttribute("aria-expanded", String(open));
}

function setAuthMode(mode) {
  state.authMode = mode;
  state.registrationChallengeId = "";
  const register = mode === "register";
  document.querySelectorAll(".register-only").forEach((item) => { item.hidden = !register; });
  document.querySelectorAll(".login-only").forEach((item) => { item.hidden = register; });
  document.querySelectorAll(".verification-only").forEach((item) => { item.hidden = true; });
  document.getElementById("authTitle").textContent = register ? "创建 Classing 账户" : "登录 Classing";
  document.getElementById("authSubtitle").textContent = register ? "注册后即可创建课表和管理会员。" : "使用邮箱或用户名继续。";
  document.getElementById("authEyebrow").textContent = register ? "GET STARTED" : "WELCOME BACK";
  document.getElementById("authSubmit").textContent = register ? "创建账户" : "登录";
  document.getElementById("authSwitch").textContent = register ? "返回登录" : "创建账户";
  document.getElementById("resetLink").hidden = register;
  document.getElementById("authPassword").autocomplete = register ? "new-password" : "current-password";
  document.getElementById("authError").textContent = "";
  updateAuthLegalLinks();
  if (register) prepareRegistrationSecurity();
}

async function prepareRegistrationSecurity() {
  try {
    await ensureRegistrationConfig();
    if (!state.registrationConfig.turnstileRequired || !state.registrationConfig.turnstileSiteKey) return;
    if (!window.turnstile) {
      await new Promise((resolve, reject) => {
        const script = document.createElement("script");
        script.src = "https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit";
        script.async = true; script.defer = true; script.onload = resolve; script.onerror = reject;
        document.head.appendChild(script);
      });
    }
    const container = document.getElementById("turnstileContainer");
    container.innerHTML = "";
    state.turnstileWidgetId = window.turnstile.render(container, { sitekey: state.registrationConfig.turnstileSiteKey });
  } catch {
    document.getElementById("authError").textContent = "人机验证组件加载失败，请稍后重试";
  }
}

async function submitAuth(event) {
  event.preventDefault();
  const button = document.getElementById("authSubmit");
  const errorNode = document.getElementById("authError");
  button.disabled = true; errorNode.textContent = "";
  try {
    if (!legalAgreementUrlsReady()) throw new Error("协议链接配置缺失，请联系管理员");
    if (!document.getElementById("authConsent").checked) throw new Error("请先勾选同意隐私政策、用户协议和个人数据跨境传输协议");
    const register = state.authMode === "register";
    const payload = register
      ? { username: document.getElementById("authUsername").value, email: document.getElementById("authEmail").value, password: document.getElementById("authPassword").value }
      : { identifier: document.getElementById("authIdentifier").value, password: document.getElementById("authPassword").value };
    payload.consent = authConsentPayload();
    let path = "/api/v1/auth/login";
    if (register && !state.registrationChallengeId) {
      path = "/api/v1/auth/register/email/request";
      payload.turnstileToken = state.turnstileWidgetId !== null && window.turnstile ? window.turnstile.getResponse(state.turnstileWidgetId) : "";
    } else if (register) {
      path = "/api/v1/auth/register/email/confirm";
      payload.challengeId = state.registrationChallengeId;
      payload.verificationCode = document.getElementById("authVerificationCode").value;
    }
    const response = await safeFetch(path, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload) });
    const body = await response.json();
    if (!response.ok) throw new Error(body.message || "认证失败");
    if (register && !body.session) {
      state.registrationChallengeId = body.challenge.challengeId;
      document.querySelectorAll(".verification-only").forEach((item) => { item.hidden = false; });
      document.getElementById("authSubtitle").textContent = body.devVerificationCode ? `验证码已发送（开发环境：${body.devVerificationCode}）` : "验证码已发送至邮箱，请完成验证。";
      button.textContent = "验证并创建账户";
      document.getElementById("authVerificationCode").focus();
      return;
    }
    saveSession(body.session);
    state.account = (await api("/api/v1/account/me")).account;
    showConsole();
  } catch (error) {
    errorNode.textContent = error.message;
    if (state.authMode === "register" && !state.registrationChallengeId && state.turnstileWidgetId !== null && window.turnstile) window.turnstile.reset(state.turnstileWidgetId);
  }
  finally { updateAuthSubmitState(); }
}

async function requestReset() {
  const email = window.prompt("请输入账户邮箱");
  if (!email) return;
  try {
    const response = await api("/api/v1/auth/password/reset/request", { method: "POST", body: JSON.stringify({ email }) }, false);
    if (response.devResetToken) {
      const password = window.prompt(`开发环境重置令牌：${response.devResetToken}\n请输入新密码`);
      if (password) await api("/api/v1/auth/password/reset/confirm", { method: "POST", body: JSON.stringify({ token: response.devResetToken, newPassword: password }) }, false);
    }
    toast("如果账户存在，重置请求已经受理");
  } catch (error) { toast(error.message, "error"); }
}

async function signOut(callAPI) {
  if (callAPI && state.session) {
    try { await api("/api/v1/auth/logout", { method: "POST", body: JSON.stringify({ refreshToken: state.session.refreshToken }) }, false); } catch { /* local sign-out still proceeds */ }
  }
  stopSettingsStream();
  saveSession(null); state.account = null; showAuth();
}

async function setView(view) {
  if (!viewCopy[view] || (!isAdmin() && ["overview", "users", "redeem", "mail", "audit", "releases", "aiAdmin"].includes(view))) view = "schedules";
  state.view = view;
  navItems.forEach((item) => item.classList.toggle("active", item.dataset.view === view));
  document.getElementById("viewCrumb").textContent = viewCopy[view][0];
  document.body.classList.remove("nav-open");
  setLoading();
  try {
    const renderers = { overview: renderOverview, users: renderUsers, schedules: renderSchedules, membership: renderMembership, askAi: renderAskAI, redeem: renderRedeem, briefings: renderBriefings, releases: renderReleases, mail: renderMail, audit: renderAudit, aiAdmin: renderAIAdmin, settings: renderSettings };
    await renderers[view]();
  } catch (error) {
    content.innerHTML = `${hero(view)}<section class="runtime-panel"><div class="empty-state"><strong>页面暂时无法加载</strong><span>${escapeHTML(error.message)}</span><button class="tonal-button" id="retryView">重试</button></div></section>`;
    document.getElementById("retryView")?.addEventListener("click", () => setView(view));
  }
}

function handleFab() {
  const destinations = { overview: isAdmin() ? "redeem" : "schedules", users: "users", schedules: "schedules", membership: "membership", redeem: "redeem", briefings: "briefings", releases: "releases", mail: "mail", audit: "audit", settings: "settings" };
  const destination = destinations[state.view] || "schedules";
  if (destination !== state.view) { setView(destination); return; }
  const target = document.querySelector(".runtime-form input, .runtime-form select");
  target?.focus();
}

function setFab(label) { document.querySelector("#materialFab strong").textContent = label; }

async function renderOverview() {
  const [dashboard, audit] = await Promise.all([api("/api/v1/admin/dashboard"), api("/api/v1/admin/audit-logs?limit=6")]);
  const stats = dashboard.stats;
  setFab("创建兑换批次");
  content.innerHTML = `<div class="runtime-page">${hero("overview", `<div class="hero-stat"><strong>${stats.users}</strong><span>注册用户</span></div>`)}
    <div class="runtime-grid">
      ${metric("注册用户", stats.users, "当前全部账户")}${metric("有效会员", stats.activeMembers, "尚未到期")}${metric("课表项目", stats.timetableProjects, "跨全部用户")}${metric("待处理任务", stats.pendingJobs, "邮件与重试队列")}
      <section class="runtime-panel full"><div class="runtime-panel-header"><h2>最近操作</h2><button class="text-action" id="openAudit">查看全部</button></div>${auditTable(audit.auditLogs)}</section>
    </div></div>`;
  document.getElementById("openAudit").addEventListener("click", () => setView("audit"));
}

function metric(label, value, help) { return `<article class="runtime-card"><span>${escapeHTML(label)}</span><strong>${escapeHTML(value)}</strong><small>${escapeHTML(help)}</small></article>`; }

async function renderUsers() {
  const response = await api("/api/v1/admin/users?limit=100");
  setFab("刷新用户");
  content.innerHTML = `<div class="runtime-page">${hero("users", `<div class="hero-stat"><strong>${response.total}</strong><span>账户总数</span></div>`)}
    <section class="runtime-panel"><div class="runtime-panel-header"><h2>用户目录</h2><span class="status-pill">${response.total} 个账户</span></div>
    <div class="table-wrap"><table class="data-table"><thead><tr><th>用户</th><th>角色</th><th>状态</th><th>创建时间</th><th>操作</th></tr></thead><tbody>
    ${response.users.map((user) => `<tr><td><strong>${escapeHTML(user.username)}</strong><br><small>${escapeHTML(user.email)}</small><br><small>${user.membership?.isMember ? `会员 ${escapeHTML(user.membership.tier)} · ${formatDate(user.membership.expiresAt)}` : "FREE"}</small></td><td><select data-user-role="${user.userId}"><option ${user.role === "USER" ? "selected" : ""}>USER</option><option ${user.role === "ADMIN" ? "selected" : ""}>ADMIN</option></select></td><td><select data-user-status="${user.userId}"><option ${user.status === "ACTIVE" ? "selected" : ""}>ACTIVE</option><option ${user.status === "DISABLED" ? "selected" : ""}>DISABLED</option></select></td><td>${formatDate(user.createdAt)}</td><td><div class="runtime-actions"><button class="tonal-button" data-save-user="${user.userId}">保存</button>${user.membership?.isMember ? `<button class="danger-button" data-revoke-membership="${user.userId}">吊销会员</button>` : ""}</div></td></tr>`).join("")}
    </tbody></table></div></section></div>`;
  document.querySelectorAll("[data-save-user]").forEach((button) => button.addEventListener("click", async () => {
    const id = button.dataset.saveUser;
    try {
      await api(`/api/v1/admin/users/${id}`, { method: "PATCH", body: JSON.stringify({ role: document.querySelector(`[data-user-role='${id}']`).value, status: document.querySelector(`[data-user-status='${id}']`).value }) });
      toast("用户权限已更新");
    } catch (error) { toast(error.message, "error"); }
  }));
  document.querySelectorAll("[data-revoke-membership]").forEach((button) => button.addEventListener("click", async () => {
    if (!confirm("确定吊销该用户的会员权益？")) return;
    try { await api("/api/v1/admin/membership/revoke", { method: "POST", body: JSON.stringify({ userId: button.dataset.revokeMembership }) }); toast("会员权益已吊销"); renderUsers(); } catch (error) { toast(error.message, "error"); }
  }));
}

async function renderSchedules() {
  const response = await api("/api/v1/timetables?limit=100");
  setFab("新建课表");
  content.innerHTML = `<div class="runtime-page">${hero("schedules", `<div class="hero-stat"><strong>${response.total}</strong><span>课表项目</span></div>`)}
    <div class="runtime-grid"><section class="runtime-panel half"><div class="runtime-panel-header"><h2>新建课表</h2></div>
      <form class="runtime-form" id="createTimetableForm"><label class="form-field full"><span>项目名称</span><input name="name" required maxlength="100" placeholder="例如：2026 秋季学期"></label><label class="form-field"><span>时区</span><input name="timezone" value="Asia/Shanghai" required></label><label class="form-field"><span>教学周数</span><input name="weekCount" type="number" value="20" min="1" max="60"></label><label class="form-field full"><span>学期开始日期</span><input name="semesterStart" type="date"></label><div class="runtime-actions full"><button class="primary-button" type="submit">创建项目</button></div></form>
    </section><section class="runtime-panel half"><div class="runtime-panel-header"><h2>项目列表</h2><span class="status-pill">${response.total}</span></div>
      ${response.projects.length ? `<div class="table-wrap"><table class="data-table"><thead><tr><th>名称</th><th>版本</th><th>更新</th><th>操作</th></tr></thead><tbody>${response.projects.map((item) => `<tr><td><strong>${escapeHTML(item.name)}</strong><br><small>${escapeHTML(item.timezone)}</small></td><td>v${item.version}</td><td>${formatDate(item.updatedAt, true)}</td><td><button class="danger-button" data-delete-project="${item.projectId}">删除</button></td></tr>`).join("")}</tbody></table></div>` : emptyState("还没有课表", "创建第一份课表后即可同步客户端数据。")}
    </section></div></div>`;
  document.getElementById("createTimetableForm").addEventListener("submit", async (event) => {
    event.preventDefault(); const data = new FormData(event.currentTarget);
    try { await api("/api/v1/timetables", { method: "POST", body: JSON.stringify({ name: data.get("name"), timezone: data.get("timezone"), semesterStart: data.get("semesterStart"), weekCount: Number(data.get("weekCount")), document: { lessons: [], exceptions: [] } }) }); toast("课表项目已创建"); renderSchedules(); }
    catch (error) { toast(error.message, "error"); }
  });
  document.querySelectorAll("[data-delete-project]").forEach((button) => button.addEventListener("click", async () => {
    if (!confirm("确定删除该课表项目？此操作不可撤销。")) return;
    try { await api(`/api/v1/timetables/${button.dataset.deleteProject}`, { method: "DELETE" }); toast("课表已删除"); renderSchedules(); } catch (error) { toast(error.message, "error"); }
  }));
}

async function renderMembership() {
  const response = await api("/api/v1/membership/status"); const item = response.membership;
  setFab(item.isMember ? "会员权益" : "兑换会员");
  content.innerHTML = `<div class="runtime-page">${hero("membership", `<div class="hero-stat"><strong>${item.isMember ? escapeHTML(item.tier) : "FREE"}</strong><span>${item.isMember ? `有效至 ${formatDate(item.expiresAt)}` : "当前方案"}</span></div>`)}
    <div class="runtime-grid"><section class="runtime-panel half"><h2>当前权益</h2><div class="empty-state"><span class="status-pill ${item.isMember ? "" : "warn"}">${item.isMember ? "会员有效" : "免费账户"}</span><strong>${item.isMember ? escapeHTML(item.tier) : "尚未订阅会员"}</strong><span>${item.isMember ? `到期时间：${formatDate(item.expiresAt, true)}` : "使用兑换码即可升级并解锁官方云同步。"}</span></div></section>
    ${item.isMember ? "" : `<section class="runtime-panel half"><div class="runtime-panel-header"><h2>兑换会员</h2></div><form class="runtime-form" id="redeemForm"><label class="form-field full"><span>兑换码</span><input name="code" required placeholder="CLS-XXXX-XXXX-XXXX"></label><div class="runtime-actions full"><button class="primary-button">立即兑换</button></div></form></section>`}</div></div>`;
  document.getElementById("redeemForm")?.addEventListener("submit", async (event) => { event.preventDefault(); const code = new FormData(event.currentTarget).get("code"); try { await api("/api/v1/membership/redeem", { method: "POST", body: JSON.stringify({ code }) }); toast("会员权益已更新"); renderMembership(); } catch (error) { toast(error.message, "error"); } });
}

async function renderAskAI() {
  const [schedules, usageResponse, conversations] = await Promise.all([
    api("/api/v1/timetables?limit=100"), api("/api/v1/ai/usage/me"), api("/api/v1/ai/conversations?limit=30"),
  ]);
  const usage = usageResponse.usage;
  setFab("新建对话");
  const active = state.aiConversationId || conversations.conversations[0]?.conversationId || "";
  state.aiConversationId = active;
  content.innerHTML = `<div class="runtime-page">${hero("askAi", `<div class="hero-stat"><strong>${usage.limit < 0 ? "不限" : `${usage.used}/${usage.limit}`}</strong><span>本月提问</span></div>`) }
    <div class="runtime-grid"><section class="runtime-panel half"><div class="runtime-panel-header"><h2>会话</h2><button class="tonal-button" id="newAiConversation">新建</button></div>
      <label class="form-field full"><span>新对话课表</span><select id="aiSchedule">${schedules.projects.map((p) => `<option value="${p.projectId}">${escapeHTML(p.name)}</option>`).join("")}</select></label>
      <p class="form-hint">剩余 ${usage.limit < 0 ? "不限" : Math.max(0, usage.limit - usage.used - usage.reserved)} 次；下次重置 ${formatDate(usage.resetAt, true)}</p>
      <div class="ai-conversation-list">${conversations.conversations.map((item) => `<button class="tonal-button" data-ai-conversation="${item.conversationId}">${escapeHTML(item.title)}<small>${formatDate(item.updatedAt, true)}</small></button>`).join("") || "<p>还没有会话</p>"}</div>
    </section><section class="runtime-panel half"><div class="runtime-panel-header"><h2 id="aiChatTitle">${active ? "课表问答" : "新对话"}</h2><button class="danger-button" id="deleteAiConversation" ${active ? "" : "disabled"}>删除</button></div>
      <div class="ai-message-list" id="aiMessages"><div class="empty-state"><span>选择历史会话，或直接输入问题开始。</span></div></div>
      <form class="runtime-form" id="aiChatForm"><label class="form-field full"><span>问题</span><textarea name="message" required maxlength="4000" placeholder="例如：我周三下午有哪些课？"></textarea></label><div class="runtime-actions full"><button class="primary-button">发送</button></div></form>
    </section></div></div>`;
  document.querySelectorAll("[data-ai-conversation]").forEach((button) => button.addEventListener("click", async () => { state.aiConversationId = button.dataset.aiConversation; await renderAskAI(); }));
  document.getElementById("newAiConversation").addEventListener("click", () => { state.aiConversationId = ""; renderAskAI(); });
  document.getElementById("deleteAiConversation").addEventListener("click", async () => { if (!state.aiConversationId || !confirm("永久删除此会话？")) return; await api(`/api/v1/ai/conversations/${state.aiConversationId}`, { method: "DELETE" }); state.aiConversationId = ""; renderAskAI(); });
  if (active) await loadAIMessages(active);
  document.getElementById("aiChatForm").addEventListener("submit", async (event) => { event.preventDefault(); const message = new FormData(event.currentTarget).get("message").trim(); if (!message) return; await sendAIMessage(message, document.getElementById("aiSchedule").value); event.currentTarget.reset(); });
}

async function loadAIMessages(conversationId) {
  const response = await api(`/api/v1/ai/conversations/${conversationId}/messages`);
  const target = document.getElementById("aiMessages");
  target.innerHTML = response.messages.map((item) => `<article class="ai-message ${item.role === "USER" ? "user" : "assistant"}"><strong>${item.role === "USER" ? "你" : "Ask AI"}</strong><p>${escapeHTML(item.content)}</p></article>`).join("") || target.innerHTML;
  target.scrollTop = target.scrollHeight;
}

async function sendAIMessage(message, projectId) {
  const target = document.getElementById("aiMessages");
  const formButton = document.querySelector("#aiChatForm button"); formButton.disabled = true;
  target.insertAdjacentHTML("beforeend", `<article class="ai-message user"><strong>你</strong><p>${escapeHTML(message)}</p></article><article class="ai-message assistant" id="aiStreaming"><strong>Ask AI</strong><p></p></article>`);
  const stream = document.querySelector("#aiStreaming p");
  try {
    const payload = { conversationId: state.aiConversationId || undefined, clientRequestId: crypto.randomUUID(), message };
    if (!state.aiConversationId) { const project = await api(`/api/v1/timetables/${projectId}`); payload.timetableSnapshot = project.project.document; payload.sourceProjectId = projectId; }
    const headers = { "Content-Type": "application/json", "Authorization": `Bearer ${state.session.accessToken}` };
    const response = await safeFetch("/api/v1/ai/chat", { method: "POST", headers, body: JSON.stringify(payload) });
    if (!response.ok || !response.body) { const error = await response.json().catch(() => ({})); throw new Error(error.message || "Ask AI 暂时不可用"); }
    const reader = response.body.getReader(); const decoder = new TextDecoder(); let pending = ""; let event = "";
    while (true) { const { value, done } = await reader.read(); if (done) break; pending += decoder.decode(value, { stream: true }); const lines = pending.split("\n"); pending = lines.pop(); for (const line of lines) { if (line.startsWith("event:")) event = line.slice(6).trim(); if (line.startsWith("data:")) { const data = JSON.parse(line.slice(5)); if (event === "conversation") state.aiConversationId = data.conversationId; if (event === "delta") stream.textContent += data.text; if (event === "error") throw new Error(data.message || data.code); event = ""; } } }
  } catch (error) { stream.textContent = `请求失败：${error.message}`; toast(error.message, "error"); }
  finally { formButton.disabled = false; target.scrollTop = target.scrollHeight; }
}

async function renderAIAdmin() {
  const [response, usageResponse] = await Promise.all([api("/api/v1/admin/ai/config"), api("/api/v1/admin/ai/usage?limit=100")]); const item = response.config;
  setFab("保存 AI 配置");
  content.innerHTML = `<div class="runtime-page">${hero("aiAdmin")}
    <div class="runtime-grid"><section class="runtime-panel full"><div class="runtime-panel-header"><h2>提供商与 Prompt</h2><span class="status-pill">${item.secretConfigured ? "密钥已配置" : "密钥缺失"}</span></div>
      <form class="runtime-form" id="aiConfigForm"><label class="form-field"><span>启用</span><select name="enabled"><option value="0" ${!item.enabled ? "selected" : ""}>关闭</option><option value="1" ${item.enabled ? "selected" : ""}>启用</option></select></label><label class="form-field"><span>模型</span><input name="model" value="${escapeHTML(item.model)}" required></label><label class="form-field full"><span>Base URL</span><input name="baseUrl" value="${escapeHTML(item.baseUrl)}" placeholder="https://provider.example/v1" required></label><label class="form-field"><span>环境变量密钥引用</span><input name="secretRef" value="${escapeHTML(item.secretRef)}" placeholder="AI_PROVIDER_KEY_DEFAULT" required></label><label class="form-field"><span>默认月限额</span><input name="defaultMonthlyLimit" type="number" min="0" value="${Number(item.defaultMonthlyLimit)}"></label><label class="form-field"><span>最大输出 tokens</span><input name="maxOutputTokens" type="number" value="${Number(item.maxOutputTokens)}"></label><label class="form-field"><span>超时（秒）</span><input name="timeoutSeconds" type="number" value="${Number(item.timeoutSeconds)}"></label><label class="form-field"><span>历史消息上限</span><input name="maxHistoryMessages" type="number" value="${Number(item.maxHistoryMessages)}"></label><label class="form-field full"><span>System Prompt</span><textarea name="systemPrompt">${escapeHTML(item.systemPrompt)}</textarea></label><label class="form-field full"><span>课表 Prompt</span><textarea name="timetablePrompt">${escapeHTML(item.timetablePrompt)}</textarea></label><div class="runtime-actions full"><button class="primary-button">保存配置</button></div></form>
    </section><section class="runtime-panel full"><h2>批量用户限额</h2><form class="runtime-form" id="aiQuotaForm"><label class="form-field full"><span>用户 ID（逗号分隔）</span><input name="userIds" required></label><label class="form-field"><span>模式</span><select name="mode"><option>INHERIT</option><option>LIMITED</option><option>UNLIMITED</option><option>BLOCKED</option></select></label><label class="form-field"><span>LIMITED 月限额</span><input name="monthlyLimit" type="number" min="0" value="100"></label><div class="runtime-actions full"><button class="primary-button">应用限额</button></div></form></section></div></div>`;
  document.getElementById("aiConfigForm").addEventListener("submit", async (event) => { event.preventDefault(); const data = Object.fromEntries(new FormData(event.currentTarget)); await api("/api/v1/admin/ai/config", { method:"PUT", body:JSON.stringify({ enabled:Number(data.enabled), providerKind:"OPENAI_COMPATIBLE", baseUrl:data.baseUrl, model:data.model, secretRef:data.secretRef, systemPrompt:data.systemPrompt, timetablePrompt:data.timetablePrompt, temperature:0.2, maxOutputTokens:Number(data.maxOutputTokens), timeoutSeconds:Number(data.timeoutSeconds), maxHistoryMessages:Number(data.maxHistoryMessages), defaultMonthlyLimit:Number(data.defaultMonthlyLimit), quotaTimezone:"Asia/Shanghai" }) }); toast("AI 配置已保存"); renderAIAdmin(); });
  document.getElementById("aiQuotaForm").addEventListener("submit", async (event) => { event.preventDefault(); const data = Object.fromEntries(new FormData(event.currentTarget)); await api("/api/v1/admin/ai/quotas", { method:"PUT", body:JSON.stringify({ userIds:data.userIds.split(",").map((x) => x.trim()).filter(Boolean), mode:data.mode, monthlyLimit:Number(data.monthlyLimit) }) }); toast("用户限额已更新"); });
  content.querySelector(".runtime-grid").insertAdjacentHTML("beforeend", `<section class="runtime-panel full"><div class="runtime-panel-header"><h2>用户 AI 用量</h2><span class="status-pill">${usageResponse.total}</span></div><div class="table-wrap"><table class="data-table"><thead><tr><th>用户</th><th>本月</th><th>已用</th><th>预占</th><th>限额模式</th></tr></thead><tbody>${usageResponse.usage.map((row) => `<tr><td><strong>${escapeHTML(row.username)}</strong><br><small>${escapeHTML(row.email)}</small></td><td>${escapeHTML(row.period)}</td><td>${row.used}</td><td>${row.reserved}</td><td>${escapeHTML(row.mode)}${row.mode === "LIMITED" ? ` · ${row.monthlyLimit}` : ""}</td></tr>`).join("") || "<tr><td colspan=\"5\">暂无用户</td></tr>"}</tbody></table></div></section>`);
  const testButton = document.createElement("button"); testButton.type = "button"; testButton.className = "tonal-button"; testButton.textContent = "测试提供商"; document.querySelector("#aiConfigForm .runtime-actions").append(testButton); testButton.addEventListener("click", () => runButtonAction(testButton, async () => { await api("/api/v1/admin/ai/config/test", { method:"POST" }); toast("提供商连接正常"); }));
}

async function renderRedeem() {
  const response = await api("/api/v1/admin/redeem-codes/query?limit=100");
  setFab("生成兑换码");
  content.innerHTML = `<div class="runtime-page">${hero("redeem", `<div class="hero-stat"><strong>${response.total}</strong><span>兑换码记录</span></div>`)}
    <div class="runtime-grid"><section class="runtime-panel half"><h2>生成批次</h2><form class="runtime-form" id="generateCodesForm"><label class="form-field"><span>类型</span><select name="codeType"><option>UNIQUE</option><option>CAMPAIGN</option></select></label><label class="form-field"><span>数量</span><input name="count" type="number" min="1" max="500" value="1"></label><label class="form-field"><span>会员天数</span><input name="grantDays" type="number" min="1" value="30"></label><label class="form-field"><span>每码额度</span><input name="maxRedemptions" type="number" min="1" value="1"></label><div class="runtime-actions full"><button class="primary-button">生成兑换码</button></div></form><pre class="code-output" id="generatedCodes" hidden></pre></section>
    <section class="runtime-panel half"><div class="runtime-panel-header"><h2>兑换码</h2><span class="status-pill">${response.total}</span></div>${response.codes.length ? `<div class="table-wrap"><table class="data-table"><thead><tr><th>兑换码</th><th>权益</th><th>使用量</th><th>状态</th></tr></thead><tbody>${response.codes.map((item) => `<tr><td><code>${escapeHTML(item.code)}</code></td><td>${item.grantDays} 天</td><td>${item.currentRedemptions}/${item.maxRedemptions}</td><td>${item.revokedAt ? "已吊销" : "可用"}</td></tr>`).join("")}</tbody></table></div>` : emptyState("暂无兑换码", "生成后会在此展示。")}</section></div></div>`;
  document.getElementById("generateCodesForm").addEventListener("submit", async (event) => { event.preventDefault(); const data = new FormData(event.currentTarget); try { const result = await api("/api/v1/admin/redeem-codes/generate", { method: "POST", body: JSON.stringify({ codeType: data.get("codeType"), count: Number(data.get("count")), grantDays: Number(data.get("grantDays")), maxRedemptions: Number(data.get("maxRedemptions")) }) }); const output = document.getElementById("generatedCodes"); output.hidden = false; output.textContent = result.codes.map((item) => item.code).join("\n"); toast("兑换码已生成，请安全保存"); } catch (error) { toast(error.message, "error"); } });
}

async function renderBriefings() {
  const response = await api("/api/v1/briefings/daily"); const item = response.briefing;
  setFab("保存简报");
content.innerHTML = `<div class="runtime-page">${hero("briefings", `<div class="hero-stat"><strong>${item.enabled ? escapeHTML(item.time) : "OFF"}</strong><span>${item.enabled ? escapeHTML(item.timezone) : "未启用"}</span></div>`)}
<section class="runtime-panel"><form class="runtime-form" id="briefingForm"><label class="form-field"><span>启用状态</span><select name="enabled"><option value="true" ${item.enabled ? "selected" : ""}>启用</option><option value="false" ${!item.enabled ? "selected" : ""}>关闭</option></select></label><label class="form-field"><span>渠道</span><select name="channel"><option ${item.channel === "APP_NOTIFICATION" ? "selected" : ""}>APP_NOTIFICATION</option><option ${item.channel === "EMAIL" ? "selected" : ""}>EMAIL</option><option ${item.channel === "BOTH" ? "selected" : ""}>BOTH</option></select></label><label class="form-field"><span>时间</span><input name="time" type="time" value="${escapeHTML(item.time)}" required></label><label class="form-field"><span>时区</span><input name="timezone" value="${escapeHTML(item.timezone)}" required></label><div class="runtime-actions full"><button class="primary-button">保存配置</button><button class="tonal-button" type="button" id="testAppBriefing">发送 App 测试</button><button class="tonal-button" type="button" id="testEmailBriefing">发送邮件测试</button></div></form></section></div>`;
  document.getElementById("briefingForm").addEventListener("submit", async (event) => { event.preventDefault(); const data = new FormData(event.currentTarget); try { await api("/api/v1/briefings/daily", { method: "PUT", body: JSON.stringify({ enabled: data.get("enabled") === "true", channel: data.get("channel"), time: data.get("time"), timezone: data.get("timezone") }) }); toast("每日简报配置已保存"); renderBriefings(); } catch (error) { toast(error.message, "error"); } });
document.getElementById("testAppBriefing").addEventListener("click", (event) => runButtonAction(event.currentTarget, async () => { try { await api("/api/v1/briefings/daily/test", { method: "POST", body: JSON.stringify({ channel: "APP_NOTIFICATION" }) }); toast("App 测试提醒已发送"); } catch (error) { toast(error.message, "error"); } }));
document.getElementById("testEmailBriefing").addEventListener("click", (event) => runButtonAction(event.currentTarget, async () => { try { await api("/api/v1/briefings/daily/test", { method: "POST", body: JSON.stringify({ channel: "EMAIL" }) }); toast("邮件测试简报任务已进入队列"); } catch (error) { toast(error.message, "error"); } }));
}

async function renderReleases() {
  const [announcementResult, releaseResult] = await Promise.all([
    api("/api/v1/admin/announcements?limit=100"),
    api("/api/v1/admin/releases?limit=100"),
  ]);
  setFab("上传安装包");
  const announcements = announcementResult.announcements || [];
  const releases = releaseResult.releases || [];
  content.innerHTML = `<div class="runtime-page">${hero("releases", `<div class="hero-stat"><strong>${releases.length}</strong><span>版本记录</span></div>`)}
    <div class="runtime-grid">
      <section class="runtime-panel half"><div class="runtime-panel-header"><h2>发布公告</h2><span class="status-pill">客户端可见</span></div>
        <form class="runtime-form" id="announcementForm">
          <label class="form-field full"><span>标题</span><input name="title" maxlength="120" required></label>
          <label class="form-field"><span>平台</span><select name="platform"><option value="">全部客户端</option><option value="ANDROID_MOBILE">Android 手机</option><option value="ANDROID_WEAR">Wear OS</option></select></label>
          <label class="form-field"><span>优先级</span><input name="priority" type="number" value="0"></label>
          <label class="form-field full"><span>公告内容</span><textarea name="content" rows="6" maxlength="10000" required></textarea></label>
          <label class="form-field"><span>发布时间</span><input name="publishAt" type="datetime-local"></label>
          <label class="form-field"><span>过期时间（可选）</span><input name="expiresAt" type="datetime-local"></label>
          <label class="form-field full"><span><input name="active" type="checkbox" checked> 立即启用</span></label>
          <div class="runtime-actions full"><button class="primary-button">发布公告</button></div>
        </form>
      </section>
      <section class="runtime-panel half"><div class="runtime-panel-header"><h2>上传安装包</h2><span class="status-pill warn">APK</span></div>
        <form class="runtime-form" id="releaseForm" enctype="multipart/form-data">
          <label class="form-field"><span>平台</span><select name="platform"><option value="ANDROID_MOBILE">Android 手机</option><option value="ANDROID_WEAR">Wear OS</option></select></label>
          <label class="form-field"><span>渠道</span><select name="channel"><option value="STABLE">稳定版</option><option value="BETA">测试版</option></select></label>
          <label class="form-field"><span>版本号</span><input name="versionName" placeholder="1.0.5" required></label>
          <label class="form-field"><span>版本代码</span><input name="versionCode" type="number" min="1" required></label>
          <label class="form-field"><span>最低支持版本代码</span><input name="minSupportedVersionCode" type="number" min="0" value="0"></label>
          <label class="form-field"><span>发布标题</span><input name="title" value="Classing 更新" required></label>
          <label class="form-field full"><span>更新说明</span><textarea name="changelog" rows="5"></textarea></label>
          <label class="form-field full"><span>安装包</span><input name="artifact" type="file" accept=".apk,application/vnd.android.package-archive" required></label>
          <label class="form-field"><span><input name="mandatory" type="checkbox"> 强制更新</span></label>
          <label class="form-field"><span><input name="publish" type="checkbox" checked> 上传后立即发布</span></label>
          <div class="runtime-actions full"><button class="primary-button">上传安装包</button><span id="releaseUploadStatus"></span></div>
        </form>
      </section>
      <section class="runtime-panel full"><div class="runtime-panel-header"><h2>当前公告</h2><span class="status-pill">${announcements.length}</span></div>
        ${announcements.length ? `<div class="table-wrap"><table class="data-table"><thead><tr><th>标题</th><th>平台</th><th>状态</th><th>发布时间</th><th>操作</th></tr></thead><tbody>${announcements.map((item) => `<tr><td><strong>${escapeHTML(item.title)}</strong><br><small>${escapeHTML(item.content).slice(0, 80)}</small></td><td>${escapeHTML(item.platform || "ALL")}</td><td>${item.active ? "启用" : "停用"}</td><td>${formatDate(item.publishAt, true)}</td><td><button class="danger-button" data-delete-announcement="${item.announcementId}">删除</button></td></tr>`).join("")}</tbody></table></div>` : emptyState("暂无公告", "发布后客户端将在关于页读取。")}
      </section>
      <section class="runtime-panel full"><div class="runtime-panel-header"><h2>版本与安装包</h2><span class="status-pill">${releases.length}</span></div>
        ${releases.length ? `<div class="table-wrap"><table class="data-table"><thead><tr><th>版本</th><th>平台/渠道</th><th>安装包</th><th>状态</th><th>操作</th></tr></thead><tbody>${releases.map((item) => `<tr><td><strong>${escapeHTML(item.versionName)}</strong> (${item.versionCode})<br><small>${escapeHTML(item.title)}</small></td><td>${escapeHTML(item.platform)} / ${escapeHTML(item.channel)}</td><td>${escapeHTML(item.artifactFileName)}<br><small>${formatBytes(item.artifactSize)} · ${escapeHTML(item.sha256).slice(0, 12)}…</small></td><td>${escapeHTML(item.status)}</td><td><div class="runtime-actions">${item.status !== "PUBLISHED" ? `<button class="tonal-button" data-publish-release="${item.releaseId}">发布</button>` : `<a class="tonal-button" href="${item.downloadUrl}">下载</a>`}<button class="danger-button" data-delete-release="${item.releaseId}">删除</button></div></td></tr>`).join("")}</tbody></table></div>` : emptyState("暂无版本", "上传 APK 后可立即发布或保留为草稿。")}
      </section>
    </div></div>`;

  document.getElementById("announcementForm").addEventListener("submit", async (event) => {
    event.preventDefault(); const data = new FormData(event.currentTarget);
    const toMillis = (value) => value ? new Date(value).getTime() : 0;
    try {
      await api("/api/v1/admin/announcements", { method: "POST", body: JSON.stringify({ title: data.get("title"), content: data.get("content"), platform: data.get("platform"), priority: Number(data.get("priority")), active: data.get("active") === "on", publishAt: toMillis(data.get("publishAt")), expiresAt: toMillis(data.get("expiresAt")) }) });
      toast("公告已发布"); renderReleases();
    } catch (error) { toast(error.message, "error"); }
  });
  document.getElementById("releaseForm").addEventListener("submit", async (event) => {
    event.preventDefault(); const form = new FormData(event.currentTarget); const status = document.getElementById("releaseUploadStatus");
    form.set("mandatory", String(form.get("mandatory") === "on")); form.set("publish", String(form.get("publish") === "on"));
    status.textContent = "正在上传，请勿关闭页面…";
    try { await api("/api/v1/admin/releases", { method: "POST", body: form }); toast("安装包已上传"); renderReleases(); }
    catch (error) { status.textContent = ""; toast(error.message, "error"); }
  });
  document.querySelectorAll("[data-delete-announcement]").forEach((button) => button.addEventListener("click", async () => {
    if (!confirm("确定删除该公告？")) return;
    try { await api(`/api/v1/admin/announcements/${button.dataset.deleteAnnouncement}`, { method: "DELETE" }); toast("公告已删除"); renderReleases(); } catch (error) { toast(error.message, "error"); }
  }));
  document.querySelectorAll("[data-publish-release]").forEach((button) => button.addEventListener("click", async () => {
    try { await api(`/api/v1/admin/releases/${button.dataset.publishRelease}/publish`, { method: "POST", body: "{}" }); toast("版本已发布"); renderReleases(); } catch (error) { toast(error.message, "error"); }
  }));
  document.querySelectorAll("[data-delete-release]").forEach((button) => button.addEventListener("click", async () => {
    if (!confirm("确定删除该版本及安装包？")) return;
    try { await api(`/api/v1/admin/releases/${button.dataset.deleteRelease}`, { method: "DELETE" }); toast("版本已删除"); renderReleases(); } catch (error) { toast(error.message, "error"); }
  }));
}

function formatBytes(value) {
  const bytes = Number(value || 0); if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}

function parseLogDetails(details) {
  if (!details) return {};
  try { return JSON.parse(details); } catch { return { raw: details }; }
}

function logDetailsHTML(details) {
  const parsed = parseLogDetails(details);
  const entries = Object.entries(parsed).filter(([, value]) => value !== "" && value !== null && value !== undefined);
  if (!entries.length) return "";
  return `<pre class="log-json">${escapeHTML(JSON.stringify(Object.fromEntries(entries), null, 2))}</pre>`;
}

function jobLogsHTML(job, logs) {
  const items = logs?.[job.jobId] || [];
  const lastError = job.lastError ? `<div class="job-last-error"><strong>Last error</strong><span>${escapeHTML(job.lastError)}</span></div>` : "";
  if (!items.length && !lastError) return `<details class="log-details"><summary>无详细日志</summary><div class="log-empty">该任务尚未被 worker 处理。</div></details>`;
  return `<details class="log-details"><summary>查看详细日志 ${items.length ? `(${items.length})` : ""}</summary>${lastError}<div class="log-lines">${items.map((item) => `<div class="log-line ${escapeHTML(String(item.level || "").toLowerCase())}"><div><span class="status-pill">${escapeHTML(item.level)}</span><strong>${escapeHTML(item.event)}</strong><small>${formatDate(item.createdAt, true)}</small></div><p>${escapeHTML(item.message)}</p>${logDetailsHTML(item.details)}</div>`).join("")}</div></details>`;
}

async function renderMail() {
  const [mailboxes, jobs] = await Promise.all([api("/api/v1/admin/mailboxes"), api("/api/v1/admin/briefing-jobs?limit=50")]);
  setFab("添加邮箱");
  const logs = jobs.jobLogs || {};
  content.innerHTML = `<div class="runtime-page">${hero("mail", `<div class="hero-stat"><strong>${jobs.total}</strong><span>投递任务</span></div>`)}<div class="runtime-grid"><section class="runtime-panel half"><div class="runtime-panel-header"><h2>添加 SMTP 邮箱</h2><span class="status-pill">Lark Ready</span></div><form class="runtime-form" id="mailboxForm"><label class="form-field full"><span>服务商预设</span><select id="mailPreset"><option value="custom">自定义 SMTP</option><option value="lark_starttls">Lark 公共邮箱 · STARTTLS 587</option><option value="lark_ssl">Lark 公共邮箱 · SSL 465</option></select></label><p class="full form-hint">Lark 公共邮箱请在后台开启 IMAP/SMTP 服务；用户名与发件地址使用完整邮箱，密码填写为服务器环境变量引用，不在管理台录入明文。当前服务器已配置通用 SMTP Secret，可直接使用 env:CLASSING_SMTP_PASSWORD。默认使用 587 STARTTLS，以规避部分云主机的 465 出站限制。</p><label class="form-field"><span>名称</span><input name="name" required></label><label class="form-field"><span>发件地址</span><input name="fromAddress" type="email" required></label><label class="form-field"><span>SMTP Host</span><input name="smtpHost" required></label><label class="form-field"><span>端口</span><input name="smtpPort" type="number" value="587" required></label><label class="form-field"><span>用户名</span><input name="username" required></label><label class="form-field"><span>密码 Secret 引用</span><input name="passwordSecretRef" value="env:CLASSING_SMTP_PASSWORD" required></label><label class="form-field"><span>每日额度</span><input name="dailyQuota" type="number" value="450" required></label><div class="runtime-actions full"><button class="primary-button">添加邮箱</button></div></form></section>
    <section class="runtime-panel half"><h2>邮箱池</h2>${mailboxes.mailboxes.length ? `<div class="table-wrap"><table class="data-table"><thead><tr><th>邮箱</th><th>SMTP</th><th>额度</th><th>操作</th></tr></thead><tbody>${mailboxes.mailboxes.map((item) => `<tr><td>${escapeHTML(item.name)}<br><small>${escapeHTML(item.fromAddress)}</small><br><small>${escapeHTML(item.username)}</small></td><td>${escapeHTML(item.smtpHost)}:${item.smtpPort}<br><small>${item.smtpPort === 465 ? "SSL/TLS" : "STARTTLS if available"}</small></td><td>${item.usedToday}/${item.dailyQuota}</td><td><div class="runtime-actions"><button class="tonal-button" data-edit-mailbox="${item.mailboxId}">编辑</button><button class="danger-button" data-delete-mailbox="${item.mailboxId}">删除</button></div></td></tr>`).join("")}</tbody></table></div>` : emptyState("邮箱池未配置", "添加邮箱后，投递工作器会从环境变量读取密码。")}</section>
    <section class="runtime-panel full"><div class="runtime-panel-header"><h2>最近任务</h2><span class="status-pill">${jobs.total}</span></div>${jobs.jobs.length ? `<div class="table-wrap"><table class="data-table"><thead><tr><th>任务</th><th>用户</th><th>状态</th><th>投递信息</th><th>日志</th><th>操作</th></tr></thead><tbody>${jobs.jobs.map((item) => `<tr><td><strong>${escapeHTML(item.jobId)}</strong><br><small>${escapeHTML(item.channel)}</small></td><td>${escapeHTML(item.userId)}</td><td>${escapeHTML(item.status)}<br><small>retry ${item.retryCount || 0}</small></td><td>${escapeHTML(item.targetDate)}<br><small>${formatDate(item.updatedAt, true)}</small></td><td>${jobLogsHTML(item, logs)}</td><td><button class="tonal-button" data-retry-job="${item.jobId}">重试</button></td></tr>`).join("")}</tbody></table></div>` : emptyState("暂无投递任务", "测试简报或定时任务会显示在这里。")}</section></div></div>`;
  const applyPreset = (preset) => {
    const form = document.getElementById("mailboxForm");
    if (!form || preset === "custom") return;
    const ssl = preset === "lark_ssl";
    form.elements.name.value = "Lark noreply-classing";
    form.elements.fromAddress.value = "noreply-classing@zcwww.cc";
    form.elements.smtpHost.value = "smtp.larksuite.com";
    form.elements.smtpPort.value = ssl ? "465" : "587";
    form.elements.username.value = "noreply-classing@zcwww.cc";
    form.elements.passwordSecretRef.value = "env:CLASSING_SMTP_PASSWORD";
    form.elements.dailyQuota.value = "450";
  };
  document.getElementById("mailPreset").addEventListener("change", (event) => applyPreset(event.target.value));
  applyPreset("lark_starttls");
  document.getElementById("mailboxForm").addEventListener("submit", async (event) => { event.preventDefault(); const data = Object.fromEntries(new FormData(event.currentTarget)); data.smtpPort = Number(data.smtpPort); data.dailyQuota = Number(data.dailyQuota); data.enabled = 1; const id = event.currentTarget.dataset.editingId; try { await api(id ? `/api/v1/admin/mailboxes/${id}` : "/api/v1/admin/mailboxes", { method: id ? "PUT" : "POST", body: JSON.stringify(data) }); toast(id ? "邮箱信息已更新" : "邮箱已加入池中"); renderMail(); } catch (error) { toast(error.message, "error"); } });
  document.querySelectorAll("[data-edit-mailbox]").forEach((button) => button.addEventListener("click", () => {
    const item = mailboxes.mailboxes.find((mailbox) => mailbox.mailboxId === button.dataset.editMailbox); if (!item) return;
    const form = document.getElementById("mailboxForm"); form.dataset.editingId = item.mailboxId;
    ["name", "fromAddress", "smtpHost", "smtpPort", "username", "passwordSecretRef", "dailyQuota"].forEach((key) => { form.elements[key].value = item[key] ?? ""; });
    form.querySelector("button[type='submit'], button.primary-button").textContent = "保存修改"; form.scrollIntoView({ behavior: "smooth" });
  }));
  document.querySelectorAll("[data-delete-mailbox]").forEach((button) => button.addEventListener("click", async () => {
    if (!confirm("确定删除该 SMTP 邮箱？")) return;
    try { await api(`/api/v1/admin/mailboxes/${button.dataset.deleteMailbox}`, { method: "DELETE" }); toast("邮箱已删除"); renderMail(); } catch (error) { toast(error.message, "error"); }
  }));
  document.querySelectorAll("[data-retry-job]").forEach((button) => button.addEventListener("click", async () => { try { await api(`/api/v1/admin/briefing-jobs/${button.dataset.retryJob}/retry`, { method: "POST", body: "{}" }); toast("任务已重新排队"); renderMail(); } catch (error) { toast(error.message, "error"); } }));
}

async function renderAudit() {
  const response = await api("/api/v1/admin/audit-logs?limit=100"); setFab("刷新日志");
  content.innerHTML = `<div class="runtime-page">${hero("audit", `<div class="hero-stat"><strong>${response.total}</strong><span>审计事件</span></div>`)}<section class="runtime-panel">${auditTable(response.auditLogs)}</section></div>`;
}

function auditTable(items) {
  if (!items?.length) return emptyState("暂无审计记录", "完成一次账户或管理操作后会显示在这里。");
  return `<div class="table-wrap"><table class="data-table"><thead><tr><th>时间</th><th>主体</th><th>动作</th><th>对象</th><th>请求</th></tr></thead><tbody>${items.map((item) => `<tr><td>${formatDate(item.createdAt, true)}</td><td>${escapeHTML(item.actorId || "SYSTEM")}</td><td><strong>${escapeHTML(item.action)}</strong></td><td>${escapeHTML(item.targetType)}<br><small>${escapeHTML(item.targetId)}</small></td><td><code>${escapeHTML(item.requestId)}</code></td></tr>`).join("")}</tbody></table></div>`;
}

function readMobileSettings(document) {
  const values = {};
  const records = document?.records?.["mobile.settings"] || [];
  records.filter((item) => !item.deletedAt && item.payload).forEach((item) => {
    try { values[item.id] = JSON.parse(item.payload).value; } catch { /* ignore malformed record */ }
  });
  return values;
}

async function fetchCloudDocument(retry = true, minimumVersion = 0, staleAttempt = 0) {
  const headers = { Authorization: `Bearer ${state.session.accessToken}`, Accept: "application/json" };
  const response = await safeFetch("/api/v1/cloud/official/document", { headers });
  if (response.status === 401 && retry && await refreshSession()) return fetchCloudDocument(false, minimumVersion, staleAttempt);
  const text = await response.text();
  if (!response.ok) {
    let body = {}; try { body = JSON.parse(text); } catch { body.message = text; }
    const error = new Error(body.message || `HTTP ${response.status}`); error.status = response.status; throw error;
  }
  const etag = response.headers.get("ETag") || '"0"';
  const version = Number(String(etag).replace(/\D/g, "")) || 0;
  if (version < minimumVersion && staleAttempt < 3) {
    await sleep(100 * (staleAttempt + 1));
    return fetchCloudDocument(retry, minimumVersion, staleAttempt + 1);
  }
  saveCloudCursor(version);
  return { document: text ? JSON.parse(text) : {}, etag, version };
}

async function saveMobileSettings(document, etag, values, attempt = 0, retryAuth = true) {
  const baseDocument = JSON.parse(JSON.stringify(document || {}));
  document = JSON.parse(JSON.stringify(baseDocument));
  const now = Date.now();
  const deviceId = browserDeviceId();
  document.format = "classing_cloud_sync_v2";
  document.records ||= {};
  const records = document.records["mobile.settings"] ||= [];
  document.devices ||= [];
  document.changes ||= [];
  const device = document.devices.find((item) => item.deviceId === deviceId) || { deviceId, lastCounter: 0, lastChangedAt: 0 };
  let counter = Math.max(device.lastCounter || 0, ...document.devices.map((item) => Number(item.lastCounter || 0)));
  Object.entries(values).forEach(([id, value]) => {
    counter += 1;
    const record = { id, payload: JSON.stringify({ value }), version: { counter, deviceId, changedAt: now }, deletedAt: null, recoverableUntil: null };
    const index = records.findIndex((item) => item.id === id);
    if (index >= 0) records[index] = record; else records.push(record);
    document.changes.unshift({ id: `chg-web-${now}-${counter}`, domain: "mobile.settings", recordId: id, action: "updated", version: record.version, occurredAt: now, detail: "web settings" });
  });
  device.lastCounter = counter; device.lastChangedAt = now;
  const deviceIndex = document.devices.findIndex((item) => item.deviceId === deviceId);
  if (deviceIndex >= 0) document.devices[deviceIndex] = device; else document.devices.push(device);
  document.changes = document.changes.slice(0, 100);
  document.updatedAt = now;
  const response = await safeFetch("/api/v1/cloud/official/document", {
    method: "PUT",
    headers: {
      Authorization: `Bearer ${state.session.accessToken}`,
      "Content-Type": "application/json",
      "If-Match": etag,
      "Idempotency-Key": crypto.randomUUID(),
    },
    body: JSON.stringify(document),
  });
  if (response.status === 401 && retryAuth && await refreshSession()) {
    return saveMobileSettings(baseDocument, etag, values, attempt, false);
  }
  if (response.status === 412 && attempt < 3) {
    await sleep(150 * (attempt + 1));
    const latest = await fetchCloudDocument();
    return saveMobileSettings(latest.document, latest.etag, values, attempt + 1);
  }
  const body = await response.json().catch(() => ({}));
  if (!response.ok) {
    const error = new Error(body.message || `HTTP ${response.status}`);
    error.status = response.status;
    error.code = body.code;
    throw error;
  }
  saveCloudCursor(Number(body.version || 0));
  return body;
}

function stopSettingsStream() {
  state.settingsStreamAbort?.abort();
  state.settingsStreamAbort = null;
  if (state.settingsStreamRetry) clearTimeout(state.settingsStreamRetry);
  state.settingsStreamRetry = null;
}

function scheduleSettingsStream(delay = 3000) {
  if (!state.session || state.settingsStreamRetry) return;
  state.settingsStreamRetry = setTimeout(() => {
    state.settingsStreamRetry = null;
    startSettingsStream();
  }, delay);
}

async function applyCloudEventBlock(block) {
  let eventName = "message"; let eventId = ""; let data = "";
  block.split("\n").forEach((line) => {
    if (line.startsWith("event:")) eventName = line.slice(6).trim();
    else if (line.startsWith("id:")) eventId = line.slice(3).trim();
    else if (line.startsWith("data:")) data += line.slice(5).trim();
  });
  if (eventName !== "cloud-document") return;
  let payload = {}; try { payload = JSON.parse(data || "{}"); } catch { return; }
  const version = Number(payload.version ?? eventId);
  if (!Number.isSafeInteger(version) || version <= state.cloudEventVersion) return;
  saveCloudCursor(version);
  if (state.view !== "settings" || state.settingsRendering) return;
  if (state.settingsFormDirty) {
    const status = document.querySelector("#clientSettingsForm")?.closest(".runtime-panel")?.querySelector(".status-pill");
    if (status) status.textContent = "云端有新设置，保存后将自动合并";
    return;
  }
  await renderSettings();
}

async function startSettingsStream() {
  if (!state.session || state.settingsStreamAbort) return;
  const controller = new AbortController(); state.settingsStreamAbort = controller;
  let reconnectDelay = 3000;
  try {
    const headers = { Authorization: `Bearer ${state.session.accessToken}` };
    if (state.cloudEventVersion > 0) headers["Last-Event-ID"] = String(state.cloudEventVersion);
    const response = await safeFetch("/api/v1/cloud/official/events", { headers, signal: controller.signal });
    if (response.status === 401 && await refreshSession()) { reconnectDelay = 0; return; }
    if (!response.ok || !response.body) throw new Error(`SSE HTTP ${response.status}`);
    const reader = response.body.getReader(); const decoder = new TextDecoder(); let buffer = "";
    while (state.session && !controller.signal.aborted) {
      const { value, done } = await reader.read(); if (done) break;
      buffer += decoder.decode(value, { stream: true });
      let boundary = buffer.indexOf("\n\n");
      while (boundary >= 0) {
        const block = buffer.slice(0, boundary).replace(/\r/g, "");
        buffer = buffer.slice(boundary + 2);
        if (block && !block.startsWith(":")) await applyCloudEventBlock(block);
        boundary = buffer.indexOf("\n\n");
      }
      if (buffer.length > 8192) buffer = buffer.slice(-4096);
    }
  } catch (error) {
    if (error.name === "AbortError") reconnectDelay = -1;
  } finally {
    if (state.settingsStreamAbort === controller) state.settingsStreamAbort = null;
    if (reconnectDelay >= 0 && state.session) scheduleSettingsStream(reconnectDelay);
  }
}

async function renderSettings() {
  if (state.settingsRendering) return;
  state.settingsRendering = true;
  try {
  const [settings, membership] = await Promise.all([isAdmin() ? api("/api/v1/admin/settings") : Promise.resolve({ settings: {} }), api("/api/v1/membership/status")]);
  let cloud = null; let mobileSettings = {}; let cloudError = "";
  try { cloud = await fetchCloudDocument(true, state.cloudEventVersion); mobileSettings = readMobileSettings(cloud.document); } catch (error) { cloudError = error.message; }
  const settingsSyncStatus = cloud ? (membership.membership?.isMember ? "实时连接" : "仅同步设置") : "不可用";
  setFab("编辑设置");
  content.innerHTML = `<div class="runtime-page">${hero("settings", `<div class="hero-stat"><strong>${escapeHTML(state.account.role)}</strong><span>${escapeHTML(state.account.status)}</span></div>`)}<div class="runtime-grid">
    <section class="runtime-panel half"><div class="runtime-panel-header"><h2>个人资料</h2><span class="status-pill">${escapeHTML(state.account.role)}</span></div><form class="runtime-form" id="profileForm"><label class="form-field full"><span>用户名</span><input name="username" value="${escapeHTML(state.account.username)}" required minlength="3" maxlength="40"></label><label class="form-field full"><span>邮箱</span><input name="email" type="email" value="${escapeHTML(state.account.email)}" required></label><div class="runtime-actions full"><button class="primary-button">保存资料</button></div></form></section>
    <section class="runtime-panel half"><div class="runtime-panel-header"><h2>修改密码</h2><span class="status-pill warn">将撤销全部会话</span></div><form class="runtime-form" id="passwordForm"><input type="text" name="username" autocomplete="username" value="${escapeHTML(state.account.username)}" hidden><label class="form-field full"><span>当前密码</span><input name="currentPassword" type="password" autocomplete="current-password" required></label><label class="form-field full"><span>新密码</span><input name="newPassword" type="password" autocomplete="new-password" minlength="8" required></label><label class="form-field full"><span>确认新密码</span><input name="confirmPassword" type="password" autocomplete="new-password" minlength="8" required></label><div class="runtime-actions full"><button class="primary-button">更新密码</button></div></form></section>
    <section class="runtime-panel full"><div class="runtime-panel-header"><h2>注销账号</h2><span class="status-pill warn">危险操作</span></div><form class="runtime-form" id="deleteAccountForm"><p class="full">注销后账号会立即退出所有设备，邮箱和用户名将被脱敏，无法再使用原账号登录。</p><label class="form-field"><span>当前密码</span><input name="currentPassword" type="password" autocomplete="current-password" required></label><label class="form-field"><span>输入 DELETE 确认</span><input name="confirm" autocomplete="off" pattern="DELETE|注销账号" required></label><div class="runtime-actions full"><button class="quiet-button">注销账号</button></div></form></section>
    <section class="runtime-panel full"><div class="runtime-panel-header"><h2>客户端设置同步</h2><span class="status-pill">${settingsSyncStatus}</span></div>${cloud ? `<form class="runtime-form" id="clientSettingsForm"><label class="form-field"><span>显示周末</span><select name="showWeekend"><option value="true" ${mobileSettings.showWeekend !== false ? "selected" : ""}>显示</option><option value="false" ${mobileSettings.showWeekend === false ? "selected" : ""}>隐藏</option></select></label><label class="form-field"><span>周数计算</span><select name="weekNumberMode"><option value="NATURAL" ${mobileSettings.weekNumberMode !== "SEMESTER" ? "selected" : ""}>自然周</option><option value="SEMESTER" ${mobileSettings.weekNumberMode === "SEMESTER" ? "selected" : ""}>学期周</option></select></label><label class="form-field"><span>新学期开始日期</span><input name="semesterWeekStartDate" type="date" value="${escapeHTML(mobileSettings.semesterWeekStartDate || "")}"></label><label class="form-field"><span>提醒</span><select name="reminderEnabled"><option value="true" ${mobileSettings.reminderEnabled ? "selected" : ""}>启用</option><option value="false" ${!mobileSettings.reminderEnabled ? "selected" : ""}>关闭</option></select></label><label class="form-field"><span>提前提醒（分钟）</span><input name="reminderMinutes" type="number" min="5" max="60" value="${Number(mobileSettings.reminderMinutes || 15)}"></label><label class="form-field"><span>保活级别</span><select name="keepAliveLevel"><option ${mobileSettings.keepAliveLevel === "ECO" ? "selected" : ""}>ECO</option><option ${mobileSettings.keepAliveLevel !== "ECO" && mobileSettings.keepAliveLevel !== "AGGRESSIVE" ? "selected" : ""}>BALANCED</option><option ${mobileSettings.keepAliveLevel === "AGGRESSIVE" ? "selected" : ""}>AGGRESSIVE</option></select></label><label class="form-field"><span>每日简报</span><select name="dailyBriefingEnabled"><option value="true" ${mobileSettings.dailyBriefingEnabled ? "selected" : ""}>启用</option><option value="false" ${!mobileSettings.dailyBriefingEnabled ? "selected" : ""}>关闭</option></select></label><label class="form-field"><span>简报时间</span><input name="dailyBriefingTime" type="time" value="${escapeHTML(mobileSettings.dailyBriefingTime || "20:00")}"></label><div class="runtime-actions full"><button class="primary-button">保存并同步</button><span>${membership.membership?.isMember ? "变更会实时通知 Web，并在客户端下次官方云同步时合并。" : "会员已过期，官方云仅保留设置项同步；课程数据不会继续跨端同步。"}</span></div></form>` : `<div class="empty-state"><strong>设置同步不可用</strong><span>${escapeHTML(cloudError)}</span></div>`}</section>
    ${isAdmin() ? `<section class="runtime-panel full"><div class="runtime-panel-header"><h2>系统运行设置</h2><span class="status-pill">管理员</span></div><form class="runtime-form" id="systemSettingsForm"><label class="form-field"><span>开放注册</span><select name="registration.enabled"><option value="true" ${settings.settings["registration.enabled"] !== "false" ? "selected" : ""}>启用</option><option value="false" ${settings.settings["registration.enabled"] === "false" ? "selected" : ""}>关闭</option></select></label><label class="form-field"><span>简报服务</span><select name="briefing.enabled"><option value="true" ${settings.settings["briefing.enabled"] !== "false" ? "selected" : ""}>启用</option><option value="false" ${settings.settings["briefing.enabled"] === "false" ? "selected" : ""}>关闭</option></select></label><label class="form-field full"><span>维护公告</span><textarea name="maintenance.message" placeholder="留空表示无公告">${escapeHTML(settings.settings["maintenance.message"] || "")}</textarea></label><div class="runtime-actions full"><button class="primary-button">保存系统设置</button></div></form></section>` : ""}
  </div></div>`;
  document.getElementById("profileForm").addEventListener("submit", async (event) => { event.preventDefault(); const data = Object.fromEntries(new FormData(event.currentTarget)); try { state.account = (await api("/api/v1/account/me", { method: "PATCH", body: JSON.stringify(data) })).account; syncAccountChrome(); toast("个人资料已更新"); } catch (error) { toast(error.message, "error"); } });
  document.getElementById("passwordForm").addEventListener("submit", async (event) => { event.preventDefault(); const data = Object.fromEntries(new FormData(event.currentTarget)); if (data.newPassword !== data.confirmPassword) { toast("两次输入的新密码不一致", "error"); return; } try { await api("/api/v1/account/password", { method: "PUT", body: JSON.stringify({ currentPassword: data.currentPassword, newPassword: data.newPassword }) }); toast("密码已更新，请重新登录"); setTimeout(() => signOut(false), 900); } catch (error) { toast(error.message, "error"); } });
  document.getElementById("deleteAccountForm").addEventListener("submit", async (event) => { event.preventDefault(); const data = Object.fromEntries(new FormData(event.currentTarget)); if (data.confirm !== "DELETE" && data.confirm !== "注销账号") { toast("请输入 DELETE 确认注销账号", "error"); return; } if (!confirm("确认注销账号？该操作会退出所有设备，且无法用原账号继续登录。")) return; try { await api("/api/v1/account/delete", { method: "POST", body: JSON.stringify({ currentPassword: data.currentPassword, confirm: data.confirm }) }); toast("账号已注销"); setTimeout(() => signOut(false), 500); } catch (error) { toast(error.message, "error"); } });
  const clientSettingsForm = document.getElementById("clientSettingsForm");
  state.settingsFormDirty = false;
  state.settingsDirtyFields.clear();
  const markSettingsFieldDirty = (event) => {
    if (!event.target.name) return;
    state.settingsFormDirty = true;
    state.settingsDirtyFields.add(event.target.name);
  };
  clientSettingsForm?.addEventListener("input", markSettingsFieldDirty);
  clientSettingsForm?.addEventListener("change", markSettingsFieldDirty);
  clientSettingsForm?.addEventListener("submit", async (event) => { event.preventDefault(); const data = Object.fromEntries(new FormData(event.currentTarget)); const formValues = { showWeekend: data.showWeekend === "true", weekNumberMode: data.weekNumberMode, semesterWeekStartDate: data.semesterWeekStartDate, reminderEnabled: data.reminderEnabled === "true", reminderMinutes: Number(data.reminderMinutes), keepAliveLevel: data.keepAliveLevel, dailyBriefingEnabled: data.dailyBriefingEnabled === "true", dailyBriefingTime: data.dailyBriefingTime }; const values = Object.fromEntries(Array.from(state.settingsDirtyFields).filter((name) => Object.hasOwn(formValues, name)).map((name) => [name, formValues[name]])); if (!Object.keys(values).length) { toast("没有需要同步的更改"); return; } try { await saveMobileSettings(cloud.document, cloud.etag, values); state.settingsFormDirty = false; state.settingsDirtyFields.clear(); toast("客户端设置已同步"); state.settingsRendering = false; await renderSettings(); } catch (error) { toast(error.message, "error"); } });
  document.getElementById("systemSettingsForm")?.addEventListener("submit", async (event) => { event.preventDefault(); const data = Object.fromEntries(new FormData(event.currentTarget)); try { await api("/api/v1/admin/settings", { method: "PUT", body: JSON.stringify({ settings: data }) }); toast("系统设置已更新"); } catch (error) { toast(error.message, "error"); } });
  } finally {
    state.settingsRendering = false;
  }
}

boot();
