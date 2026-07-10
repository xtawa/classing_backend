const state = {
  session: readSession(),
  account: null,
  view: "overview",
  authMode: "login",
  refreshing: null,
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
  redeem: ["兑换码", "REDEMPTION", "以批次管理权益发放", "创建唯一兑换码或活动码，并追踪额度与核销状态。"],
  briefings: ["每日简报", "BRIEFING", "在正确时间送达下一天课程", "选择邮箱渠道、投递时间和时区，并随时发送测试任务。"],
  releases: ["公告与版本", "RELEASES", "从一个入口发布公告与安装包", "上传 APK、发布稳定版本，并控制客户端可见的公告。"],
  mail: ["邮件与任务", "DELIVERY", "观察邮箱池与投递队列", "管理员可以配置 SMTP Secret 引用并重试失败任务。"],
  audit: ["审计日志", "AUDIT", "关键操作都有可检索轨迹", "账户、权益、同步与系统设置变化都会留下记录。"],
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

async function api(path, options = {}, retry = true) {
  const headers = new Headers(options.headers || {});
  if (options.body && !(options.body instanceof FormData) && !headers.has("Content-Type")) headers.set("Content-Type", "application/json");
  if (state.session?.accessToken) headers.set("Authorization", `Bearer ${state.session.accessToken}`);
  const response = await fetch(path, { ...options, headers });
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
      const response = await fetch("/api/v1/auth/refresh", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ refreshToken: state.session.refreshToken }) });
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
  document.getElementById("authIdentifier").focus();
}

function showConsole() {
  authShell.hidden = true;
  prototype.hidden = false;
  document.querySelector(".service-state").innerHTML = `<span class="connected"></span>服务已连接`;
  syncAccountChrome();
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
  const register = mode === "register";
  document.querySelectorAll(".register-only").forEach((item) => { item.hidden = !register; });
  document.querySelectorAll(".login-only").forEach((item) => { item.hidden = register; });
  document.getElementById("authTitle").textContent = register ? "创建 Classing 账户" : "登录 Classing";
  document.getElementById("authSubtitle").textContent = register ? "注册后即可创建课表和管理会员。" : "使用邮箱或用户名继续。";
  document.getElementById("authEyebrow").textContent = register ? "GET STARTED" : "WELCOME BACK";
  document.getElementById("authSubmit").textContent = register ? "创建账户" : "登录";
  document.getElementById("authSwitch").textContent = register ? "返回登录" : "创建账户";
  document.getElementById("resetLink").hidden = register;
  document.getElementById("authPassword").autocomplete = register ? "new-password" : "current-password";
  document.getElementById("authError").textContent = "";
}

async function submitAuth(event) {
  event.preventDefault();
  const button = document.getElementById("authSubmit");
  const errorNode = document.getElementById("authError");
  button.disabled = true; errorNode.textContent = "";
  try {
    const register = state.authMode === "register";
    const payload = register
      ? { username: document.getElementById("authUsername").value, email: document.getElementById("authEmail").value, password: document.getElementById("authPassword").value }
      : { identifier: document.getElementById("authIdentifier").value, password: document.getElementById("authPassword").value };
    const response = await fetch(`/api/v1/auth/${register ? "register" : "login"}`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload) });
    const body = await response.json();
    if (!response.ok) throw new Error(body.message || "认证失败");
    saveSession(body.session);
    state.account = (await api("/api/v1/account/me")).account;
    showConsole();
  } catch (error) { errorNode.textContent = error.message; }
  finally { button.disabled = false; }
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
  saveSession(null); state.account = null; showAuth();
}

async function setView(view) {
  if (!viewCopy[view] || (!isAdmin() && ["overview", "users", "redeem", "mail", "audit", "releases"].includes(view))) view = "schedules";
  state.view = view;
  navItems.forEach((item) => item.classList.toggle("active", item.dataset.view === view));
  document.getElementById("viewCrumb").textContent = viewCopy[view][0];
  document.body.classList.remove("nav-open");
  setLoading();
  try {
    const renderers = { overview: renderOverview, users: renderUsers, schedules: renderSchedules, membership: renderMembership, redeem: renderRedeem, briefings: renderBriefings, releases: renderReleases, mail: renderMail, audit: renderAudit, settings: renderSettings };
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
    ${response.users.map((user) => `<tr><td><strong>${escapeHTML(user.username)}</strong><br><small>${escapeHTML(user.email)}</small></td><td><select data-user-role="${user.userId}"><option ${user.role === "USER" ? "selected" : ""}>USER</option><option ${user.role === "ADMIN" ? "selected" : ""}>ADMIN</option></select></td><td><select data-user-status="${user.userId}"><option ${user.status === "ACTIVE" ? "selected" : ""}>ACTIVE</option><option ${user.status === "DISABLED" ? "selected" : ""}>DISABLED</option></select></td><td>${formatDate(user.createdAt)}</td><td><button class="tonal-button" data-save-user="${user.userId}">保存</button></td></tr>`).join("")}
    </tbody></table></div></section></div>`;
  document.querySelectorAll("[data-save-user]").forEach((button) => button.addEventListener("click", async () => {
    const id = button.dataset.saveUser;
    try {
      await api(`/api/v1/admin/users/${id}`, { method: "PATCH", body: JSON.stringify({ role: document.querySelector(`[data-user-role='${id}']`).value, status: document.querySelector(`[data-user-status='${id}']`).value }) });
      toast("用户权限已更新");
    } catch (error) { toast(error.message, "error"); }
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
  setFab("兑换会员");
  content.innerHTML = `<div class="runtime-page">${hero("membership", `<div class="hero-stat"><strong>${item.isMember ? escapeHTML(item.tier) : "FREE"}</strong><span>${item.isMember ? `有效至 ${formatDate(item.expiresAt)}` : "当前方案"}</span></div>`)}
    <div class="runtime-grid"><section class="runtime-panel half"><h2>当前权益</h2><div class="empty-state"><span class="status-pill ${item.isMember ? "" : "warn"}">${item.isMember ? "会员有效" : "免费账户"}</span><strong>${item.isMember ? escapeHTML(item.tier) : "尚未订阅会员"}</strong><span>${item.isMember ? `到期时间：${formatDate(item.expiresAt, true)}` : "使用兑换码即可升级并解锁官方云同步。"}</span></div></section>
    <section class="runtime-panel half"><div class="runtime-panel-header"><h2>兑换会员</h2></div><form class="runtime-form" id="redeemForm"><label class="form-field full"><span>兑换码</span><input name="code" required placeholder="CLS-XXXX-XXXX-XXXX"></label><div class="runtime-actions full"><button class="primary-button">立即兑换</button></div></form></section></div></div>`;
  document.getElementById("redeemForm").addEventListener("submit", async (event) => { event.preventDefault(); const code = new FormData(event.currentTarget).get("code"); try { await api("/api/v1/membership/redeem", { method: "POST", body: JSON.stringify({ code }) }); toast("会员权益已更新"); renderMembership(); } catch (error) { toast(error.message, "error"); } });
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
    <section class="runtime-panel"><form class="runtime-form" id="briefingForm"><label class="form-field"><span>启用状态</span><select name="enabled"><option value="true" ${item.enabled ? "selected" : ""}>启用</option><option value="false" ${!item.enabled ? "selected" : ""}>关闭</option></select></label><label class="form-field"><span>渠道</span><select name="channel"><option ${item.channel === "APP_NOTIFICATION" ? "selected" : ""}>APP_NOTIFICATION</option><option ${item.channel === "EMAIL" ? "selected" : ""}>EMAIL</option><option ${item.channel === "BOTH" ? "selected" : ""}>BOTH</option></select></label><label class="form-field"><span>时间</span><input name="time" type="time" value="${escapeHTML(item.time)}" required></label><label class="form-field"><span>时区</span><input name="timezone" value="${escapeHTML(item.timezone)}" required></label><div class="runtime-actions full"><button class="primary-button">保存配置</button><button class="tonal-button" type="button" id="testBriefing">发送测试</button></div></form></section></div>`;
  document.getElementById("briefingForm").addEventListener("submit", async (event) => { event.preventDefault(); const data = new FormData(event.currentTarget); try { await api("/api/v1/briefings/daily", { method: "PUT", body: JSON.stringify({ enabled: data.get("enabled") === "true", channel: data.get("channel"), time: data.get("time"), timezone: data.get("timezone") }) }); toast("每日简报配置已保存"); renderBriefings(); } catch (error) { toast(error.message, "error"); } });
  document.getElementById("testBriefing").addEventListener("click", async () => { try { await api("/api/v1/briefings/daily/test", { method: "POST", body: "{}" }); toast("测试简报任务已进入队列"); } catch (error) { toast(error.message, "error"); } });
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

async function renderMail() {
  const [mailboxes, jobs] = await Promise.all([api("/api/v1/admin/mailboxes"), api("/api/v1/admin/briefing-jobs?limit=50")]);
  setFab("添加邮箱");
  content.innerHTML = `<div class="runtime-page">${hero("mail", `<div class="hero-stat"><strong>${jobs.total}</strong><span>投递任务</span></div>`)}<div class="runtime-grid"><section class="runtime-panel half"><h2>添加 SMTP 邮箱</h2><form class="runtime-form" id="mailboxForm"><label class="form-field"><span>名称</span><input name="name" required></label><label class="form-field"><span>发件地址</span><input name="fromAddress" type="email" required></label><label class="form-field"><span>SMTP Host</span><input name="smtpHost" required></label><label class="form-field"><span>端口</span><input name="smtpPort" type="number" value="587" required></label><label class="form-field"><span>用户名</span><input name="username" required></label><label class="form-field"><span>密码 Secret 引用</span><input name="passwordSecretRef" value="env:SMTP_PASSWORD" required></label><label class="form-field"><span>每日额度</span><input name="dailyQuota" type="number" value="500" required></label><div class="runtime-actions full"><button class="primary-button">添加邮箱</button></div></form></section>
    <section class="runtime-panel half"><h2>邮箱池</h2>${mailboxes.mailboxes.length ? `<div class="table-wrap"><table class="data-table"><thead><tr><th>邮箱</th><th>Host</th><th>额度</th></tr></thead><tbody>${mailboxes.mailboxes.map((item) => `<tr><td>${escapeHTML(item.name)}<br><small>${escapeHTML(item.fromAddress)}</small></td><td>${escapeHTML(item.smtpHost)}:${item.smtpPort}</td><td>${item.usedToday}/${item.dailyQuota}</td></tr>`).join("")}</tbody></table></div>` : emptyState("邮箱池未配置", "添加邮箱后，投递工作器会从环境变量读取密码。")}</section>
    <section class="runtime-panel full"><div class="runtime-panel-header"><h2>最近任务</h2><span class="status-pill">${jobs.total}</span></div>${jobs.jobs.length ? `<div class="table-wrap"><table class="data-table"><thead><tr><th>任务</th><th>用户</th><th>状态</th><th>日期</th><th>重试</th></tr></thead><tbody>${jobs.jobs.map((item) => `<tr><td>${escapeHTML(item.jobId)}</td><td>${escapeHTML(item.userId)}</td><td>${escapeHTML(item.status)}</td><td>${escapeHTML(item.targetDate)}</td><td><button class="tonal-button" data-retry-job="${item.jobId}">重试</button></td></tr>`).join("")}</tbody></table></div>` : emptyState("暂无投递任务", "测试简报或定时任务会显示在这里。")}</section></div></div>`;
  document.getElementById("mailboxForm").addEventListener("submit", async (event) => { event.preventDefault(); const data = Object.fromEntries(new FormData(event.currentTarget)); data.smtpPort = Number(data.smtpPort); data.dailyQuota = Number(data.dailyQuota); data.enabled = 1; try { await api("/api/v1/admin/mailboxes", { method: "POST", body: JSON.stringify(data) }); toast("邮箱已加入池中"); renderMail(); } catch (error) { toast(error.message, "error"); } });
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

async function renderSettings() {
  const settings = isAdmin() ? await api("/api/v1/admin/settings") : { settings: {} };
  setFab("编辑设置");
  content.innerHTML = `<div class="runtime-page">${hero("settings", `<div class="hero-stat"><strong>${escapeHTML(state.account.role)}</strong><span>${escapeHTML(state.account.status)}</span></div>`)}<div class="runtime-grid">
    <section class="runtime-panel half"><div class="runtime-panel-header"><h2>个人资料</h2><span class="status-pill">${escapeHTML(state.account.role)}</span></div><form class="runtime-form" id="profileForm"><label class="form-field full"><span>用户名</span><input name="username" value="${escapeHTML(state.account.username)}" required minlength="3" maxlength="40"></label><label class="form-field full"><span>邮箱</span><input name="email" type="email" value="${escapeHTML(state.account.email)}" required></label><div class="runtime-actions full"><button class="primary-button">保存资料</button></div></form></section>
    <section class="runtime-panel half"><div class="runtime-panel-header"><h2>修改密码</h2><span class="status-pill warn">将撤销全部会话</span></div><form class="runtime-form" id="passwordForm"><input type="text" name="username" autocomplete="username" value="${escapeHTML(state.account.username)}" hidden><label class="form-field full"><span>当前密码</span><input name="currentPassword" type="password" autocomplete="current-password" required></label><label class="form-field full"><span>新密码</span><input name="newPassword" type="password" autocomplete="new-password" minlength="8" required></label><label class="form-field full"><span>确认新密码</span><input name="confirmPassword" type="password" autocomplete="new-password" minlength="8" required></label><div class="runtime-actions full"><button class="primary-button">更新密码</button></div></form></section>
    ${isAdmin() ? `<section class="runtime-panel full"><div class="runtime-panel-header"><h2>系统运行设置</h2><span class="status-pill">管理员</span></div><form class="runtime-form" id="systemSettingsForm"><label class="form-field"><span>开放注册</span><select name="registration.enabled"><option value="true" ${settings.settings["registration.enabled"] !== "false" ? "selected" : ""}>启用</option><option value="false" ${settings.settings["registration.enabled"] === "false" ? "selected" : ""}>关闭</option></select></label><label class="form-field"><span>简报服务</span><select name="briefing.enabled"><option value="true" ${settings.settings["briefing.enabled"] !== "false" ? "selected" : ""}>启用</option><option value="false" ${settings.settings["briefing.enabled"] === "false" ? "selected" : ""}>关闭</option></select></label><label class="form-field full"><span>维护公告</span><textarea name="maintenance.message" placeholder="留空表示无公告">${escapeHTML(settings.settings["maintenance.message"] || "")}</textarea></label><div class="runtime-actions full"><button class="primary-button">保存系统设置</button></div></form></section>` : ""}
  </div></div>`;
  document.getElementById("profileForm").addEventListener("submit", async (event) => { event.preventDefault(); const data = Object.fromEntries(new FormData(event.currentTarget)); try { state.account = (await api("/api/v1/account/me", { method: "PATCH", body: JSON.stringify(data) })).account; syncAccountChrome(); toast("个人资料已更新"); } catch (error) { toast(error.message, "error"); } });
  document.getElementById("passwordForm").addEventListener("submit", async (event) => { event.preventDefault(); const data = Object.fromEntries(new FormData(event.currentTarget)); if (data.newPassword !== data.confirmPassword) { toast("两次输入的新密码不一致", "error"); return; } try { await api("/api/v1/account/password", { method: "PUT", body: JSON.stringify({ currentPassword: data.currentPassword, newPassword: data.newPassword }) }); toast("密码已更新，请重新登录"); setTimeout(() => signOut(false), 900); } catch (error) { toast(error.message, "error"); } });
  document.getElementById("systemSettingsForm")?.addEventListener("submit", async (event) => { event.preventDefault(); const data = Object.fromEntries(new FormData(event.currentTarget)); try { await api("/api/v1/admin/settings", { method: "PUT", body: JSON.stringify({ settings: data }) }); toast("系统设置已更新"); } catch (error) { toast(error.message, "error"); } });
}

boot();
