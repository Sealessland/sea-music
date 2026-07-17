const $ = (selector, root = document) => root.querySelector(selector);
const $$ = (selector, root = document) => [...root.querySelectorAll(selector)];

const state = {
  accessToken: localStorage.getItem("sea_access_token") || "",
  refreshToken: localStorage.getItem("sea_refresh_token") || "",
  user: null,
  feed: "hot",
  items: [],
  visibleItems: [],
  category: "all",
  activeVideo: null,
  refreshing: null,
  feedRequestID: 0,
  videoRequestID: 0,
};

const feedMeta = {
  hot: { title: "正在流行", kicker: "TRENDING NOW", path: "/api/v1/feed/hot?limit=18" },
  recommendations: { title: "为你推荐", kicker: "MADE FOR YOU", path: "/api/v1/feed/recommendations?limit=18", auth: true },
  following: { title: "关注动态", kicker: "FOLLOWING", path: "/api/v1/feed/following?limit=18", auth: true },
};

const reasonLabels = {
  hot_window: "实时热度",
  hot_snapshot_fallback: "热榜精选",
  followed_creator: "来自关注",
  category_affinity: "兴趣相似",
  popular: "大家都在看",
  fresh: "新鲜发布",
  cold_start_recent: "新内容",
  recent_fallback: "新鲜发布",
};

const categoryGradients = [
  "linear-gradient(135deg,#173b57,#1bb7c8)",
  "linear-gradient(135deg,#322457,#9b6ed7)",
  "linear-gradient(135deg,#4c2438,#ff7894)",
  "linear-gradient(135deg,#1f3c33,#54b993)",
  "linear-gradient(135deg,#3f3521,#e0a94f)",
];

function escapeHTML(value = "") {
  return String(value).replace(/[&<>'"]/g, char => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", "'": "&#39;", '"': "&quot;" }[char]));
}

function safeMediaURL(value) {
  if (!value) return "";
  try {
    const url = new URL(value, location.origin);
    return ["http:", "https:"].includes(url.protocol) ? url.href : "";
  } catch {
    return "";
  }
}

function saveSession(pair) {
  state.accessToken = pair.access_token;
  state.refreshToken = pair.refresh_token;
  localStorage.setItem("sea_access_token", state.accessToken);
  localStorage.setItem("sea_refresh_token", state.refreshToken);
}

function clearSession() {
  state.accessToken = "";
  state.refreshToken = "";
  state.user = null;
  localStorage.removeItem("sea_access_token");
  localStorage.removeItem("sea_refresh_token");
  renderAccount();
}

async function refreshSession() {
  if (!state.refreshToken) throw new Error("登录状态已过期");
  if (!state.refreshing) {
    state.refreshing = fetch("/api/v1/sessions/refresh", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ refresh_token: state.refreshToken }),
    }).then(async response => {
      if (!response.ok) throw new Error("登录状态已过期");
      const pair = await response.json();
      saveSession(pair);
      return pair;
    }).finally(() => { state.refreshing = null; });
  }
  return state.refreshing;
}

async function api(path, options = {}, retry = true) {
  const headers = new Headers(options.headers || {});
  if (state.accessToken) headers.set("Authorization", `Bearer ${state.accessToken}`);
  if (options.body && !(options.body instanceof FormData) && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }
  // Video detail responses contain short-lived signed media URLs. Bypass the
  // browser HTTP cache so reopening a video always obtains a fresh signature.
  let response = await fetch(path, { cache: "no-store", ...options, headers });
  if (response.status === 401 && retry && state.refreshToken) {
    try {
      await refreshSession();
      return api(path, options, false);
    } catch {
      clearSession();
    }
  }
  if (response.status === 204) return null;
  const contentType = response.headers.get("Content-Type") || "";
  const body = contentType.includes("json") ? await response.json() : await response.text();
  if (!response.ok) {
    const error = new Error(body?.error?.message || body || `请求失败（${response.status}）`);
    error.status = response.status;
    error.code = body?.error?.code;
    throw error;
  }
  return body;
}

function toast(message, kind = "success") {
  const node = $("#toast");
  node.textContent = message;
  node.className = `toast show ${kind === "error" ? "error" : ""}`;
  clearTimeout(toast.timer);
  toast.timer = setTimeout(() => { node.className = "toast"; }, 2600);
}

async function copyText(value) {
  if (navigator.clipboard && window.isSecureContext) {
    await navigator.clipboard.writeText(value);
    return;
  }
  const area = document.createElement("textarea");
  area.value = value;
  area.setAttribute("readonly", "");
  area.style.position = "fixed";
  area.style.left = "-9999px";
  document.body.appendChild(area);
  area.select();
  let ok = false;
  try { ok = document.execCommand("copy"); } finally { area.remove(); }
  if (!ok) throw new Error("copy failed");
}

function requireAuth(action) {
  if (state.user) return true;
  openAuth("login");
  toast(`登录后可以${action}`, "error");
  return false;
}

function renderAccount() {
  const authenticated = Boolean(state.user);
  $("#login-button").classList.toggle("hidden", authenticated);
  $("#register-button").classList.toggle("hidden", authenticated);
  $("#user-menu").classList.toggle("hidden", !authenticated);
  $("#user-popover").classList.add("hidden");
  $("#user-menu").setAttribute("aria-expanded", "false");
  if (!authenticated) return;
  const name = state.user.username || "Sea 用户";
  $("#user-name").textContent = name;
  $("#user-avatar").textContent = name.slice(0, 1).toUpperCase();
  $("#popover-name").textContent = name;
  $("#popover-email").textContent = state.user.email || state.user.role || "member";
}

async function restoreSession() {
  if (!state.accessToken) return;
  try {
    const response = await api("/api/v1/me");
    state.user = response.user;
  } catch {
    clearSession();
  }
  renderAccount();
}

function showSkeletons() {
  $("#empty-state").classList.add("hidden");
  $("#video-grid").innerHTML = Array.from({ length: 9 }, () => `
    <article class="skeleton-card"><div class="thumbnail"></div><div class="line"></div><div class="line short"></div></article>
  `).join("");
}

async function hydrateItems(items) {
  return Promise.all(items.map(async item => {
    try {
      const detail = await api(`/api/v1/videos/${encodeURIComponent(item.id)}`);
      return { ...item, ...detail.video };
    } catch {
      return item;
    }
  }));
}

async function loadFeed(kind = state.feed) {
  const config = feedMeta[kind];
  if (!config) return;
  const requestID = ++state.feedRequestID;
  if (config.auth && !state.user) {
    openAuth("login");
    toast("登录后解锁个性内容", "error");
    return;
  }
  state.feed = kind;
  $$(".nav-link").forEach(button => button.classList.toggle("active", button.dataset.feed === kind));
  $("#feed-title").textContent = config.title;
  $("#feed-kicker").textContent = config.kicker;
  $("#feed-status").textContent = "读取中";
  $("#refresh-feed").classList.add("loading");
  $("#video-grid").setAttribute("aria-busy", "true");
  showSkeletons();
  try {
    const page = await api(config.path);
    const items = await hydrateItems(page.items || []);
    if (requestID !== state.feedRequestID) return;
    state.items = items;
    state.category = "all";
    $$(".category-pill").forEach(button => button.classList.toggle("active", button.dataset.category === "all"));
    applyFilters();
    $("#feed-status").textContent = page.degraded ? "降级数据源" : `${state.items.length} 个内容`;
    $("#api-state").textContent = "已连接";
    $("#event-state").textContent = "运行正常";
  } catch (error) {
    if (requestID !== state.feedRequestID) return;
    $("#api-state").textContent = "连接失败";
    $("#event-state").textContent = "等待 API";
    state.items = [];
    applyFilters();
    toast(error.message, "error");
  } finally {
    if (requestID === state.feedRequestID) {
      $("#refresh-feed").classList.remove("loading");
      $("#video-grid").setAttribute("aria-busy", "false");
    }
  }
}

function applyFilters() {
  const query = $("#search-input").value.trim().toLowerCase();
  state.visibleItems = state.items.filter(item => {
    const categoryMatch = state.category === "all" || String(item.category || "").toLowerCase().includes(state.category);
    const text = `${item.title || ""} ${item.description || ""} ${item.category || ""}`.toLowerCase();
    return categoryMatch && (!query || text.includes(query));
  });
  renderFeed();
}

function formatDate(value) {
  if (!value) return "最近发布";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "最近发布";
  const days = Math.floor((Date.now() - date.getTime()) / 86400000);
  if (days <= 0) return "今天";
  if (days === 1) return "昨天";
  if (days < 30) return `${days} 天前`;
  return new Intl.DateTimeFormat("zh-CN", { month: "short", day: "numeric" }).format(date);
}

function renderFeed() {
  const grid = $("#video-grid");
  const empty = $("#empty-state");
  const query = $("#search-input").value.trim();
  $("#clear-feed-search")?.classList.toggle("hidden", !query);
  if (!state.visibleItems.length) {
    grid.innerHTML = "";
    empty.classList.remove("hidden");
    renderHero(null);
    renderRanking([]);
    return;
  }
  empty.classList.add("hidden");
  grid.innerHTML = state.visibleItems.map((item, index) => {
    const title = escapeHTML(item.title || "未命名视频");
    const category = escapeHTML(item.category || "original");
    const coverURL = safeMediaURL(item.cover_url);
    const cover = coverURL ? `<img src="${escapeHTML(coverURL)}" alt="" loading="lazy">` : "";
    return `<article class="video-card" data-video-id="${escapeHTML(item.id || "")}" tabindex="0" role="button" aria-label="播放 ${title}">
      <div class="thumbnail ${cover ? "" : "no-image"}" data-initial="${escapeHTML((item.category || "SEA").slice(0, 3).toUpperCase())}" style="--card-gradient:${categoryGradients[index % categoryGradients.length]}">
        ${cover}<span class="category-badge" data-cat="${escapeHTML(String(item.category || "").toLowerCase())}">${category}</span>
        <span class="play-fab"><svg viewBox="0 0 24 24"><path d="m9 7 8 5-8 5z"/></svg></span>
      </div>
      <div class="video-info"><h3>${title}</h3><div class="video-subline"><span>${escapeHTML(item.description || "Sea Music 创作者")}</span><span class="reason">${escapeHTML(reasonLabels[item.reason_code] || formatDate(item.published_at))}</span></div></div>
    </article>`;
  }).join("");
  $$(".thumbnail img", grid).forEach(image => image.addEventListener("error", () => {
    image.closest(".thumbnail").classList.add("no-image");
    image.remove();
  }));
  $$(".video-card", grid).forEach(card => {
    card.addEventListener("click", () => openVideo(card.dataset.videoId));
    card.addEventListener("keydown", event => {
      if (event.key === "Enter" || event.key === " ") {
        event.preventDefault();
        openVideo(card.dataset.videoId);
      }
    });
  });
  renderHero(state.visibleItems[0]);
  renderRanking(state.visibleItems.slice(0, 7));
}

function renderHero(item) {
  const hero = $("#hero");
  if (!item) {
    hero.classList.add("loading");
    $("#hero-title").textContent = "等待下一阵内容浪潮";
    $("#hero-description").textContent = "登录或切换频道，发现更多真实内容。";
    $("#hero-play").disabled = true;
    hero.style.removeProperty("--hero-cover");
    return;
  }
  hero.classList.remove("loading");
  $("#hero-title").textContent = item.title || "Sea Music 精选";
  $("#hero-description").textContent = item.description || "来自 Sea Music 创作者的最新内容。";
  $("#hero-category").textContent = String(item.category || "SEA ORIGINAL").toUpperCase();
  $("#hero-date").textContent = formatDate(item.published_at);
  $("#hero-play").disabled = false;
  $("#hero-play").dataset.videoId = item.id;
  const coverURL = safeMediaURL(item.cover_url);
  if (coverURL) hero.style.setProperty("--hero-cover", `url("${coverURL.replaceAll('"', "%22")}")`);
  else hero.style.removeProperty("--hero-cover");
}

function renderRanking(items) {
  $("#ranking-list").innerHTML = items.map(item => `<li class="ranking-item" data-video-id="${escapeHTML(item.id)}"><div><strong>${escapeHTML(item.title)}</strong><small>${escapeHTML(item.category || "原创")} · ${formatDate(item.published_at)}</small></div></li>`).join("") || `<li class="comment-empty">暂无热播内容</li>`;
  $$(".ranking-item").forEach(item => item.addEventListener("click", () => openVideo(item.dataset.videoId)));
}

function resetVideoInteractions() {
  const like = $("#like-button");
  const favorite = $("#favorite-button");
  const follow = $("#follow-button");
  like.classList.remove("active");
  favorite.classList.remove("active");
  like.querySelector("span").textContent = "♡";
  favorite.querySelector("span").textContent = "☆";
  like.disabled = true;
  favorite.disabled = true;
  follow.classList.remove("active");
  follow.dataset.creatorId = "";
  follow.textContent = "＋ 关注";
  follow.disabled = true;
}

function isCurrentVideo(videoID, requestID) {
  return state.videoRequestID === requestID && state.activeVideo?.id === videoID;
}

async function openVideo(videoID) {
  if (!videoID) return;
  const requestID = ++state.videoRequestID;
  const dialog = $("#video-dialog");
  const player = $("#video-player");
  player.pause();
  player.removeAttribute("src");
  player.removeAttribute("poster");
  player.load();
  state.activeVideo = { id: videoID };
  resetVideoInteractions();
  $("#player-message").classList.add("hidden");
  $("#detail-title").textContent = "正在加载…";
  $("#detail-description").textContent = "正在读取内容详情…";
  $("#comment-count").textContent = "读取中";
  $("#comment-list").innerHTML = `<div class="comment-empty">读取评论中…</div>`;
  $("#danmaku-stage").replaceChildren();
  if (!dialog.open) dialog.showModal();
  try {
    const detail = await api(`/api/v1/videos/${encodeURIComponent(videoID)}`);
    if (!isCurrentVideo(videoID, requestID)) return;
    const item = state.items.find(value => value.id === videoID) || {};
    state.activeVideo = { ...item, ...detail.video };
    const video = state.activeVideo;
    player.src = safeMediaURL(video.playback_url);
    player.poster = safeMediaURL(video.cover_url);
    $("#detail-title").textContent = video.title || "未命名视频";
    $("#detail-description").textContent = video.description || "这位创作者还没有留下简介。";
    $("#detail-category").textContent = String(video.category || item.category || "SEA ORIGINAL").toUpperCase();
    $("#creator-name").textContent = `创作者 ${String(video.creator_id || "").slice(0, 8)}`;
    $("#creator-avatar").textContent = String(video.creator_id || "C").slice(0, 1).toUpperCase();
    $("#publish-time").textContent = `${formatDate(video.published_at)}发布`;
    $("#follow-button").dataset.creatorId = video.creator_id || "";
    $("#like-button").disabled = false;
    $("#favorite-button").disabled = false;
    $("#follow-button").disabled = false;
    await Promise.all([loadComments(videoID, requestID), loadDanmaku(videoID, requestID)]);
    if (!isCurrentVideo(videoID, requestID)) return;
    player.play().catch(() => {});
  } catch (error) {
    if (!isCurrentVideo(videoID, requestID)) return;
    $("#detail-title").textContent = "内容暂时无法播放";
    showPlayerMessage(error.message);
  }
}

function showPlayerMessage(message) {
  const node = $("#player-message");
  node.textContent = message;
  node.classList.remove("hidden");
}

async function loadComments(videoID, requestID = state.videoRequestID) {
  try {
    const page = await api(`/api/v1/videos/${encodeURIComponent(videoID)}/comments?limit=50`);
    if (!isCurrentVideo(videoID, requestID)) return;
    const comments = page.items || [];
    $("#comment-count").textContent = `${comments.length} 条`;
    $("#comment-list").innerHTML = comments.length ? comments.map(renderComment).join("") : `<div class="comment-empty">成为第一个认真评论的人</div>`;
  } catch (error) {
    if (!isCurrentVideo(videoID, requestID)) return;
    $("#comment-list").innerHTML = `<div class="comment-empty">${escapeHTML(error.message)}</div>`;
  }
}

function renderComment(comment) {
  const body = comment.deleted ? "该评论已删除" : comment.body;
  return `<article class="comment"><span class="avatar comment-avatar">${escapeHTML(String(comment.author_id || "U").slice(0, 1).toUpperCase())}</span><div><strong>用户 ${escapeHTML(String(comment.author_id || "").slice(0, 6))}</strong><p>${escapeHTML(body)}</p><time>${formatDate(comment.created_at)}</time>${(comment.replies || []).map(renderComment).join("")}</div></article>`;
}

async function loadDanmaku(videoID, requestID = state.videoRequestID) {
  try {
    const page = await api(`/api/v1/videos/${encodeURIComponent(videoID)}/danmaku?start_ms=0&end_ms=300000&limit=100`);
    if (!isCurrentVideo(videoID, requestID)) return;
    renderDanmaku(page.items || []);
  } catch {
    if (isCurrentVideo(videoID, requestID)) renderDanmaku([]);
  }
}

function renderDanmaku(messages) {
  const stage = $("#danmaku-stage");
  stage.innerHTML = messages.slice(0, 16).map((message, index) => `<span class="danmaku-line" style="top:${10 + (index % 5) * 18}%;animation-delay:${(index % 7) * .72}s">${escapeHTML(message.body)}</span>`).join("");
}

function openAuth(tab = "login") {
  switchAuthTab(tab);
  const dialog = $("#auth-dialog");
  if (!dialog.open) dialog.showModal();
}

function switchAuthTab(tab) {
  $$("[data-auth-tab]").forEach(button => button.classList.toggle("active", button.dataset.authTab === tab));
  $("#login-form").classList.toggle("hidden", tab !== "login");
  $("#register-form").classList.toggle("hidden", tab !== "register");
  $("#auth-error").textContent = "";
}

async function login(identity, password) {
  const pair = await api("/api/v1/sessions", { method: "POST", body: JSON.stringify({ identity, password }) }, false);
  saveSession(pair);
  const profile = await api("/api/v1/me");
  state.user = profile.user;
  renderAccount();
}

function bindEvents() {
  $$(".nav-link").forEach(button => button.addEventListener("click", () => loadFeed(button.dataset.feed)));
  $$(".category-pill").forEach(button => button.addEventListener("click", () => {
    state.category = button.dataset.category;
    $$(".category-pill").forEach(item => item.classList.toggle("active", item === button));
    applyFilters();
  }));
  $("#refresh-feed").addEventListener("click", () => loadFeed());
  $("#search-input").addEventListener("input", applyFilters);
  document.addEventListener("keydown", event => {
    if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
      event.preventDefault();
      $("#search-input").focus();
    }
    if (event.key === "Escape") {
      $("#user-popover").classList.add("hidden");
      $("#user-menu").setAttribute("aria-expanded", "false");
    }
  });
  $("#hero-play").addEventListener("click", event => openVideo(event.currentTarget.dataset.videoId));
  $("#login-button").addEventListener("click", () => openAuth("login"));
  $("#register-button").addEventListener("click", () => openAuth("register"));
  $$('[data-open-auth]').forEach(button => button.addEventListener("click", () => openAuth(button.dataset.openAuth)));
  $$("[data-auth-tab]").forEach(button => button.addEventListener("click", () => switchAuthTab(button.dataset.authTab)));
  $$("[data-close-dialog]").forEach(button => button.addEventListener("click", () => button.closest("dialog").close()));
  $$("dialog").forEach(dialog => dialog.addEventListener("click", event => { if (event.target === dialog) dialog.close(); }));
  $("#video-dialog").addEventListener("close", () => {
    $("#video-player").pause();
    $("#video-player").removeAttribute("src");
    $("#video-player").load();
    state.videoRequestID++;
    state.activeVideo = null;
    resetVideoInteractions();
  });
  $("#video-player").addEventListener("error", () => {
    if (state.activeVideo) showPlayerMessage("这条演示数据尚未挂载媒体对象；真实投稿完成转码后会在这里直接播放。");
  });
  $("#user-menu").addEventListener("click", event => {
    event.stopPropagation();
    const popover = $("#user-popover");
    const hidden = popover.classList.toggle("hidden");
    $("#user-menu").setAttribute("aria-expanded", String(!hidden));
  });
  document.addEventListener("click", event => {
    if (event.target.closest("#user-popover, #user-menu")) return;
    $("#user-popover").classList.add("hidden");
    $("#user-menu").setAttribute("aria-expanded", "false");
  });
  $("#logout-button").addEventListener("click", () => { clearSession(); state.feed = "hot"; loadFeed("hot"); toast("已退出登录"); });

  $("#login-form").addEventListener("submit", async event => {
    event.preventDefault();
    const submit = $("button[type=submit]", event.currentTarget);
    submit.disabled = true;
    try {
      const form = new FormData(event.currentTarget);
      await login(form.get("identity"), form.get("password"));
      $("#auth-dialog").close();
      toast(`欢迎回来，${state.user.username}`);
      loadFeed("recommendations");
    } catch (error) {
      $("#auth-error").textContent = error.message;
    } finally { submit.disabled = false; }
  });

  $("#register-form").addEventListener("submit", async event => {
    event.preventDefault();
    const submit = $("button[type=submit]", event.currentTarget);
    submit.disabled = true;
    try {
      const form = new FormData(event.currentTarget);
      const input = { username: form.get("username"), email: form.get("email"), password: form.get("password") };
      await api("/api/v1/users", { method: "POST", body: JSON.stringify(input) }, false);
      await login(input.email, input.password);
      $("#auth-dialog").close();
      toast(`账号创建成功，欢迎 ${state.user.username}`);
      loadFeed("recommendations");
    } catch (error) {
      $("#auth-error").textContent = error.message;
    } finally { submit.disabled = false; }
  });

  $("#like-button").addEventListener("click", async event => relationAction(event.currentTarget, "like", "点赞"));
  $("#favorite-button").addEventListener("click", async event => relationAction(event.currentTarget, "favorite", "收藏"));
  $("#follow-button").addEventListener("click", async event => {
    if (!requireAuth("关注创作者") || !event.currentTarget.dataset.creatorId) return;
    event.currentTarget.disabled = true;
    try {
      const result = await api(`/api/v1/users/${encodeURIComponent(event.currentTarget.dataset.creatorId)}/follow`, { method: "PUT" });
      event.currentTarget.classList.toggle("active", result.exists);
      event.currentTarget.textContent = result.exists ? "✓ 已关注" : "＋ 关注";
      toast(result.exists ? "已关注创作者" : "操作完成");
    } catch (error) { toast(error.message, "error"); }
    finally { event.currentTarget.disabled = false; }
  });
  $("#share-button").addEventListener("click", async () => {
    if (!state.activeVideo?.id) return;
    const url = `${location.origin}/?video=${encodeURIComponent(state.activeVideo.id)}`;
    try { await copyText(url); toast("链接已复制"); }
    catch { toast("浏览器未允许复制", "error"); }
  });
  $("#clear-feed-search")?.addEventListener("click", () => {
    $("#search-input").value = "";
    applyFilters();
    $("#search-input").focus();
  });
  $("#comment-form").addEventListener("submit", submitComment);
  $("#danmaku-form").addEventListener("submit", submitDanmaku);
}

async function relationAction(button, relation, label) {
  if (!requireAuth(label)) return;
  const videoID = state.activeVideo?.id;
  if (!videoID) return;
  const enabled = !button.classList.contains("active");
  button.disabled = true;
  try {
    const result = await api(`/api/v1/videos/${encodeURIComponent(videoID)}/${relation}`, { method: enabled ? "PUT" : "DELETE" });
    button.classList.toggle("active", result.exists);
    button.querySelector("span").textContent = result.exists ? (relation === "like" ? "♥" : "★") : (relation === "like" ? "♡" : "☆");
    toast(result.exists ? `已${label}` : `已取消${label}`);
  } catch (error) { toast(error.message, "error"); }
  finally { button.disabled = false; }
}

async function submitComment(event) {
  event.preventDefault();
  if (!requireAuth("发表评论")) return;
  const formElement = event.currentTarget;
  const form = new FormData(formElement);
  const body = String(form.get("body") || "").trim();
  if (!body || !state.activeVideo) return;
  const videoID = state.activeVideo.id;
  const requestID = state.videoRequestID;
  try {
    await api(`/api/v1/videos/${encodeURIComponent(videoID)}/comments`, { method: "POST", body: JSON.stringify({ body }) });
    formElement.reset();
    if (isCurrentVideo(videoID, requestID)) await loadComments(videoID, requestID);
    toast("评论已发布");
  } catch (error) { toast(error.message, "error"); }
}

async function submitDanmaku(event) {
  event.preventDefault();
  if (!requireAuth("发送弹幕")) return;
  const formElement = event.currentTarget;
  const form = new FormData(formElement);
  const body = String(form.get("body") || "").trim();
  if (!body || !state.activeVideo) return;
  const videoID = state.activeVideo.id;
  const requestID = state.videoRequestID;
  const positionMS = Math.max(0, Math.round($("#video-player").currentTime * 1000));
  try {
    await api(`/api/v1/videos/${encodeURIComponent(videoID)}/danmaku`, { method: "POST", body: JSON.stringify({ position_ms: positionMS, body }) });
    formElement.reset();
    if (isCurrentVideo(videoID, requestID)) await loadDanmaku(videoID, requestID);
    toast("弹幕已发出");
  } catch (error) { toast(error.message, "error"); }
}

async function boot() {
  bindEvents();
  await restoreSession();
  await loadFeed("hot");
  const videoID = new URLSearchParams(location.search).get("video");
  if (videoID) openVideo(videoID);
}

boot();
