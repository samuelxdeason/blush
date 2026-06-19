import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  Models, Videos, VideosBySite, Search, RecentlyDownloaded, RecentlyWatched,
  MarkWatched, Enqueue, Enumerate, Queue, RemoveJob, ClearFinished,
  CookieStatus, ConnectCookies, OpenFolder,
  MediaRootPath, ChooseMediaRoot, RestartApp, Stats,
} from "../wailsjs/go/main/App";
import { EventsOn, BrowserOpenURL } from "../wailsjs/runtime/runtime";
import { library, downloader } from "../wailsjs/go/models";

type Model = library.Model;
type Video = library.Video;
type Job = downloader.Job;

const mediaURL = (p?: string) => (p ? `/media?p=${encodeURIComponent(p)}` : "");
const SITE_LABEL: Record<string, string> = { Twitter: "X / Twitter", PornHub: "Pornhub" };
const label = (s: string) => SITE_LABEL[s] || s;
const defaultMode = (s: string): ViewMode => (s === "Twitter" ? "all" : "models");

type RemoteItem = downloader.RemoteItem;
type ViewMode = "models" | "all";
type Route =
  | { kind: "source"; site: string }
  | { kind: "recent" }
  | { kind: "watched" }
  | { kind: "browse" }
  | { kind: "downloads" }
  | { kind: "settings" };

function fmtDur(s?: number) {
  if (!s) return "";
  const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), sec = Math.floor(s % 60);
  const mm = String(m).padStart(h ? 2 : 1, "0");
  return (h ? `${h}:` : "") + `${mm}:${String(sec).padStart(2, "0")}`;
}
function fmtTotal(s?: number) {
  if (!s) return "";
  const h = Math.floor(s / 3600), m = Math.round((s % 3600) / 60);
  return h ? `${h}h ${m}m` : `${m}m`;
}
function fmtSize(b?: number) {
  if (!b) return "";
  const gb = b / 1073741824;
  return gb >= 1 ? `${gb.toFixed(1)} GB` : `${Math.round(b / 1048576)} MB`;
}

const STATUS_COLORS: Record<string, string> = {
  queued: "bg-edge text-muted",
  downloading: "bg-accent text-white",
  done: "bg-emerald-500 text-emerald-950",
  duplicate: "bg-amber-500 text-amber-950",
  error: "bg-rose-600 text-white",
};

function accentFor(route: Route): string {
  if (route.kind === "source") {
    if (route.site === "PornHub") return "#ff9000"; // Pornhub orange
    if (route.site === "Twitter") return "#e7e9ea"; // X white
  }
  return "#ff2d77"; // sultry rose default
}

function XLogo({ className = "" }: { className?: string }) {
  return (
    <svg viewBox="0 0 24 24" className={className} fill="currentColor" aria-hidden>
      <path d="M18.244 2.25h3.308l-7.227 8.26 8.502 11.24H16.17l-5.214-6.817L4.99 21.75H1.68l7.73-8.835L1.254 2.25H8.08l4.713 6.231zm-1.161 17.52h1.833L7.084 4.126H5.117z" />
    </svg>
  );
}

function SourceBadge({ site, size = "sm" }: { site: string; size?: "sm" | "lg" }) {
  const big = size === "lg";
  if (site === "PornHub")
    return (
      <span className={`font-extrabold tracking-tight ${big ? "text-2xl" : "text-[15px]"}`}>
        <span className="text-white">Porn</span>
        <span className="bg-[#ff9000] text-black rounded px-1">hub</span>
      </span>
    );
  if (site === "Twitter")
    return (
      <span className={`inline-flex items-center gap-2 font-bold ${big ? "text-2xl" : "text-[15px]"}`}>
        <XLogo className={big ? "w-6 h-6" : "w-4 h-4"} /> <span>Twitter</span>
      </span>
    );
  return <span className={big ? "text-2xl font-bold" : "font-medium"}>{label(site)}</span>;
}

export default function App() {
  const [models, setModels] = useState<Model[]>([]);
  const [route, setRoute] = useState<Route>({ kind: "recent" });
  const [mode, setMode] = useState<ViewMode>("models");
  const [selected, setSelected] = useState<Model | null>(null);
  const [videos, setVideos] = useState<Video[]>([]);
  const [search, setSearch] = useState("");
  const [searchResults, setSearchResults] = useState<Video[] | null>(null);
  const [playing, setPlaying] = useState<Video | null>(null);
  const [queue, setQueue] = useState<Job[]>([]);

  const loadModels = useCallback(() => { Models().then((m) => setModels(m || [])); }, []);

  // Load the video list for the current route (model page / flat feeds).
  const loadVideos = useCallback(() => {
    if (route.kind === "source") {
      if (mode === "all") VideosBySite(route.site).then((v) => setVideos(v || []));
      else if (selected) Videos(selected.site, selected.uploader).then((v) => setVideos(v || []));
      else setVideos([]);
    } else if (route.kind === "recent") {
      RecentlyDownloaded().then((v) => setVideos(v || []));
    } else if (route.kind === "watched") {
      RecentlyWatched().then((v) => setVideos(v || []));
    }
  }, [route, mode, selected]);

  useEffect(() => { loadModels(); Queue().then((q) => setQueue(q || [])); }, [loadModels]);
  useEffect(() => { loadVideos(); }, [loadVideos]);

  useEffect(() => {
    const offQueue = EventsOn("queue", (j: Job[]) => {
      setQueue(j || []);
      if ((j || []).some((x) => x.status === "done")) { loadModels(); loadVideos(); }
    });
    const offProg = EventsOn("progress", (p: Job) =>
      setQueue((q) => q.map((x) => (x.id === p.id ? { ...x, percent: p.percent, speed: p.speed, eta: p.eta } : x))));
    return () => { offQueue(); offProg(); };
  }, [loadModels, loadVideos]);

  // Debounced search.
  const searchTimer = useRef<number>();
  useEffect(() => {
    window.clearTimeout(searchTimer.current);
    if (!search.trim()) { setSearchResults(null); return; }
    searchTimer.current = window.setTimeout(() => Search(search.trim()).then((v) => setSearchResults(v || [])), 200);
  }, [search]);

  const sources = useMemo(() => {
    const m = new Map<string, number>();
    models.forEach((x) => m.set(x.site, (m.get(x.site) || 0) + x.count));
    return [...m.entries()].sort((a, b) => b[1] - a[1]);
  }, [models]);

  const goSource = (site: string) => { setRoute({ kind: "source", site }); setMode(defaultMode(site)); setSelected(null); setSearch(""); };
  const goView = (r: Route) => { setRoute(r); setSelected(null); setSearch(""); };
  const play = (v: Video) => { setPlaying(v); MarkWatched(v.site, v.id); };

  const activeDownloads = queue.filter((j) => j.status === "downloading" || j.status === "queued").length;

  return (
    <div className="flex h-full" style={{ ["--ac" as any]: accentFor(route) }}>
      <nav className="w-56 shrink-0 bg-[#0d0d12]/95 border-r border-edge flex flex-col overflow-y-auto">
        <div className="px-5 py-5 text-xl font-extrabold tracking-tight">
          <span className="bg-gradient-to-r from-[#ff2d77] to-[#ff9000] bg-clip-text text-transparent">Media Vault</span>
        </div>

        <SideLabel>Sources</SideLabel>
        {sources.map(([site, count]) => (
          <SideItem key={site} active={route.kind === "source" && route.site === site} onClick={() => goSource(site)}>
            <span className="flex items-center justify-between w-full">
              <SourceBadge site={site} />
              <span className="text-muted text-xs">{count}</span>
            </span>
          </SideItem>
        ))}

        <SideLabel>Browse</SideLabel>
        <SideItem active={route.kind === "recent"} onClick={() => goView({ kind: "recent" })}>🕑 Recently added</SideItem>
        <SideItem active={route.kind === "watched"} onClick={() => goView({ kind: "watched" })}>▶ Recently watched</SideItem>
        <SideItem active={route.kind === "browse"} onClick={() => goView({ kind: "browse" })}>✨ Sync</SideItem>
        <SideItem active={route.kind === "downloads"} onClick={() => goView({ kind: "downloads" })}>
          ↓ Downloads {activeDownloads > 0 && <span style={{ color: "var(--ac)" }}>({activeDownloads})</span>}
        </SideItem>
        <SideItem active={route.kind === "settings"} onClick={() => goView({ kind: "settings" })}>⚙ Settings</SideItem>

        <div className="mt-auto px-5 py-4 text-xs text-muted">
          {models.reduce((n, m) => n + m.count, 0)} videos · {models.length} models
        </div>
      </nav>

      <main className="flex-1 min-w-0 overflow-y-auto">
        {route.kind === "downloads"
          ? <Downloads queue={queue} />
          : route.kind === "settings"
            ? <SettingsPage />
            : route.kind === "browse"
              ? <BrowseSync onEnqueued={() => goView({ kind: "downloads" })} />
              : <Browser {...{ route, mode, setMode, models, selected, setSelected, videos, search, setSearch, searchResults, onOpenModel: (m: Model) => { setSelected(m); }, onPlay: play }} />}
      </main>

      {playing && <Player video={playing} onClose={() => setPlaying(null)} />}
    </div>
  );
}

function SideLabel({ children }: any) {
  return <div className="px-5 pt-4 pb-1 text-[11px] uppercase tracking-wider text-muted/70">{children}</div>;
}
function SideItem({ active, onClick, children }: any) {
  return (
    <button onClick={onClick} style={active ? { borderColor: "var(--ac)" } : undefined}
      className={`text-left px-5 py-2.5 text-sm font-medium transition border-l-2 ${active ? "bg-panel2 text-white" : "border-transparent text-muted hover:text-white hover:bg-panel2/50"}`}>
      {children}
    </button>
  );
}

/* ---------------- Browser (sources + recent + watched) ---------------- */

function Browser({ route, mode, setMode, models, selected, setSelected, videos, search, setSearch, searchResults, onOpenModel, onPlay }: any) {
  const showSearch = searchResults !== null;
  const isSource = route.kind === "source";
  const sourceModels = isSource ? models.filter((m: Model) => m.site === route.site) : [];

  let heading = "";
  if (showSearch) heading = "Search";
  else if (route.kind === "recent") heading = "Recently added";
  else if (route.kind === "watched") heading = "Recently watched";
  else if (isSource) heading = selected ? selected.uploader : label(route.site);

  return (
    <div className="p-6">
      <div className="flex items-center gap-4 mb-5">
        {isSource && mode === "models" && selected && !showSearch && (
          <button onClick={() => setSelected(null)} className="text-muted hover:text-white text-sm">← Models</button>
        )}
        {isSource && !selected && !showSearch
          ? <SourceBadge site={route.site} size="lg" />
          : <h1 className="text-xl font-semibold">{heading}</h1>}

        {isSource && !selected && !showSearch && (
          <div className="flex rounded-lg overflow-hidden border border-edge text-xs">
            <Toggle on={mode === "models"} onClick={() => setMode("models")}>By model</Toggle>
            <Toggle on={mode === "all"} onClick={() => setMode("all")}>All videos</Toggle>
          </div>
        )}

        <input value={search} onChange={(e) => setSearch(e.target.value)} placeholder="Search…"
          className="ml-auto w-64 bg-panel border border-edge rounded-lg px-4 py-2 text-sm outline-none focus:border-accent" />
      </div>

      {showSearch
        ? <VideoGrid videos={searchResults} onPlay={onPlay} />
        : isSource && mode === "models" && !selected
          ? <ModelGrid models={sourceModels} onOpen={onOpenModel} />
          : <VideoGrid videos={videos} onPlay={onPlay} />}
    </div>
  );
}

function Toggle({ on, onClick, children }: any) {
  return <button onClick={onClick} style={on ? { background: "var(--ac)", color: "#0a0a0a" } : undefined}
    className={`px-3 py-1.5 ${on ? "font-semibold" : "bg-panel text-muted hover:text-white"}`}>{children}</button>;
}

function ModelGrid({ models, onOpen }: { models: Model[]; onOpen: (m: Model) => void }) {
  if (!models.length) return <Empty>No models here yet.</Empty>;
  return (
    <div className="grid gap-4" style={{ gridTemplateColumns: "repeat(auto-fill,minmax(210px,1fr))" }}>
      {models.map((m) => (
        <div key={m.site + "/" + m.uploader} onClick={() => onOpen(m)} className="tile group">
          <div className="aspect-[4/5]">
            {m.thumbnail
              ? <img src={mediaURL(m.thumbnail)} loading="lazy" decoding="async" className="w-full h-full object-cover" />
              : <div className="w-full h-full grid place-items-center text-muted text-4xl bg-panel2">▦</div>}
          </div>
          <div className="overlay" />
          <div className="absolute bottom-0 left-0 right-0 p-3">
            <div className="font-semibold truncate drop-shadow">{m.uploader}</div>
            <div className="text-xs text-white/65 mt-0.5">{m.count} video{m.count === 1 ? "" : "s"}{m.totalSeconds ? ` · ${fmtTotal(m.totalSeconds)}` : ""}{m.bytes ? ` · ${fmtSize(m.bytes)}` : ""}</div>
          </div>
        </div>
      ))}
    </div>
  );
}

function VideoGrid({ videos, onPlay }: { videos: Video[]; onPlay: (v: Video) => void }) {
  if (!videos?.length) return <Empty>No videos here.</Empty>;
  return (
    <div className="grid gap-4" style={{ gridTemplateColumns: "repeat(auto-fill,minmax(230px,1fr))" }}>
      {videos.map((v) => <VideoCard key={v.site + "/" + v.id} v={v} onPlay={onPlay} />)}
    </div>
  );
}

function VideoCard({ v, onPlay }: { v: Video; onPlay: (v: Video) => void }) {
  return (
    <div onClick={() => onPlay(v)} className="tile group">
      <div className="aspect-[4/5]">
        {v.thumbnail
          ? <img src={mediaURL(v.thumbnail)} loading="lazy" decoding="async" className="w-full h-full object-cover" />
          : <div className="w-full h-full grid place-items-center text-muted text-4xl bg-panel2">▶</div>}
      </div>
      <div className="overlay" />
      <div className="playbtn">
        <span className="w-14 h-14 rounded-full bg-black/55 backdrop-blur grid place-items-center text-xl pl-1"
          style={{ boxShadow: "0 0 0 2px var(--ac)" }}>▶</span>
      </div>
      {v.duration ? <span className="absolute top-2 right-2 bg-black/75 text-white text-[11px] px-1.5 py-0.5 rounded">{fmtDur(v.duration)}</span> : null}
      {v.webpage_url && (
        <button title="Open original page"
          onClick={(e) => { e.stopPropagation(); BrowserOpenURL(v.webpage_url); }}
          className="absolute top-2 left-2 bg-black/70 hover:bg-white/25 text-white text-xs w-7 h-7 rounded-full opacity-0 group-hover:opacity-100 transition">↗</button>
      )}
      <div className="absolute bottom-0 left-0 right-0 p-3">
        <div className="text-sm font-semibold line-clamp-2 leading-snug drop-shadow">{v.title || v.uploader}</div>
        <div className="text-xs text-white/60 mt-1 truncate">{v.uploader}{v.height ? ` · ${v.height}p` : ""}{v.filesize ? ` · ${fmtSize(v.filesize)}` : ""}</div>
      </div>
    </div>
  );
}

function Player({ video, onClose }: { video: Video; onClose: () => void }) {
  const [copied, setCopied] = useState(false);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === "Escape") onClose(); };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);
  const copy = () => { try { navigator.clipboard?.writeText(video.webpage_url); setCopied(true); setTimeout(() => setCopied(false), 1500); } catch {} };
  return (
    <div className="fixed inset-0 bg-black/90 z-50 flex flex-col" onClick={onClose}>
      <div className="flex items-center gap-3 px-5 py-3 text-sm" onClick={(e) => e.stopPropagation()}>
        <button onClick={onClose} className="text-muted hover:text-white">✕ Close</button>
        <span className="font-medium truncate">{video.title || video.uploader}</span>
        <div className="ml-auto flex gap-4 shrink-0">
          {video.webpage_url && <button onClick={copy} className="text-muted hover:text-white">{copied ? "Copied!" : "Copy link"}</button>}
          {video.webpage_url && <button onClick={() => BrowserOpenURL(video.webpage_url)} className="text-muted hover:text-white">Source ↗</button>}
          <button onClick={() => OpenFolder(video.filepath)} className="text-muted hover:text-white">Show file</button>
        </div>
      </div>
      <div className="flex-1 min-h-0 px-5" onClick={(e) => e.stopPropagation()}>
        <video src={mediaURL(video.filepath)} controls autoPlay className="w-full h-full object-contain bg-black rounded-lg" />
      </div>
      <div className="px-5 py-2 text-xs text-muted" onClick={(e) => e.stopPropagation()}>
        {[label(video.site), video.uploader, video.height ? `${video.height}p` : "", fmtSize(video.filesize), video.upload_date]
          .filter(Boolean).join("  ·  ")}
      </div>
    </div>
  );
}

/* ---------------- Browse / Sync ---------------- */

function BrowseSync({ onEnqueued }: { onEnqueued: () => void }) {
  const [url, setUrl] = useState("");
  const [items, setItems] = useState<RemoteItem[] | null>(null);
  const [loading, setLoading] = useState(false);
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [status, setStatus] = useState({ x: false, pornhub: false });
  const [fav, setFav] = useState(localStorage.phUser || "");

  useEffect(() => { CookieStatus().then(setStatus); }, []);

  const loadURL = async (u: string) => {
    u = u.trim(); if (!u) return;
    setLoading(true); setItems(null); setPicked(new Set());
    try { setItems((await Enumerate(u)) || []); }
    catch { setItems([]); }
    finally { setLoading(false); }
  };
  const loadFavorites = () => {
    const user = fav.trim(); if (!user) return;
    localStorage.phUser = user;
    loadURL(`https://www.pornhub.com/users/${user}/videos/favorites`);
  };

  const toggle = (u: string) =>
    setPicked((p) => { const n = new Set(p); n.has(u) ? n.delete(u) : n.add(u); return n; });
  const newItems = (items || []).filter((i) => !i.owned);
  const ownedCount = (items || []).length - newItems.length;
  const download = () => { picked.forEach((u) => Enqueue(u)); onEnqueued(); };

  return (
    <div className="p-6 max-w-3xl">
      <h1 className="text-xl font-semibold mb-1">Sync</h1>
      <p className="text-sm text-muted mb-4">
        Pull in a Pornhub model, channel, or playlist — pick what you want and it downloads into your library.
      </p>

      <div className="flex gap-2 max-w-3xl">
        <input value={url} onChange={(e) => setUrl(e.target.value)} onKeyDown={(e) => { if (e.key === "Enter") loadURL(url); }}
          placeholder="https://www.pornhub.com/model/NAME/videos"
          className="flex-1 bg-panel border border-edge rounded-lg px-4 py-3 text-sm outline-none focus:border-accent" />
        <button onClick={() => loadURL(url)} disabled={loading} style={{ background: "var(--ac)", color: "#0a0a0a" }}
          className="font-semibold px-6 rounded-lg disabled:opacity-50">{loading ? "Loading…" : "Load"}</button>
      </div>

      <div className="flex items-center gap-2 mt-3 text-sm max-w-3xl">
        <span className="text-muted">★ Your favorites:</span>
        <input value={fav} onChange={(e) => setFav(e.target.value)} onKeyDown={(e) => { if (e.key === "Enter") loadFavorites(); }}
          placeholder="pornhub username"
          className="w-48 bg-panel border border-edge rounded-lg px-3 py-2 outline-none focus:border-accent" />
        <button onClick={loadFavorites} disabled={!status.pornhub || !fav.trim()}
          className="text-xs font-semibold px-3 py-2 rounded-lg bg-edge hover:bg-panel2 disabled:opacity-40">Sync my favorites</button>
        {!status.pornhub && <span className="text-xs text-amber-400">Connect Pornhub in Downloads first</span>}
      </div>

      {loading && <Empty>Loading list…</Empty>}

      {items && items.length > 0 && (
        <>
          <div className="flex items-center gap-3 mt-6 mb-3 text-sm">
            <span className="font-semibold">{items.length} videos</span>
            <span className="text-muted">· {ownedCount} owned · {newItems.length} new · {picked.size} selected</span>
            <button onClick={() => setPicked(new Set(newItems.map((i) => i.url)))}
              className="ml-auto text-xs text-muted hover:text-white">Select all new</button>
            <button onClick={download} disabled={!picked.size} style={{ background: "var(--ac)", color: "#0a0a0a" }}
              className="text-xs font-semibold px-4 py-2 rounded-lg disabled:opacity-40">Download {picked.size || ""}</button>
          </div>
          <div className="space-y-1">
            {items.map((it) => (
              <label key={it.url}
                className={`flex items-center gap-3 px-3 py-2 rounded-lg ${it.owned ? "opacity-50" : "hover:bg-panel2 cursor-pointer"}`}>
                {it.owned
                  ? <span className="text-xs text-emerald-400 w-16 shrink-0">in library</span>
                  : <input type="checkbox" checked={picked.has(it.url)} onChange={() => toggle(it.url)} className="w-4 h-4 accent-pink-500 shrink-0" />}
                <span className="text-sm truncate">{it.title || it.url}</span>
              </label>
            ))}
          </div>
        </>
      )}
      {items && items.length === 0 && !loading &&
        <Empty>No videos found — check the URL, or connect Pornhub for private lists.</Empty>}
    </div>
  );
}

/* ---------------- Settings ---------------- */

function SettingsPage() {
  const [root, setRoot] = useState("");
  const [changed, setChanged] = useState(false);
  const [stats, setStats] = useState<library.Stats | null>(null);

  useEffect(() => { MediaRootPath().then(setRoot); Stats().then(setStats); }, []);

  const change = async () => {
    const next = await ChooseMediaRoot();
    if (next && next !== root) { setRoot(next); setChanged(true); }
  };

  const siteColor = (s: string) => (s === "PornHub" ? "#ff9000" : s === "Twitter" ? "#e7e9ea" : "#ff2d77");

  return (
    <div className="p-6 max-w-3xl">
      <h1 className="text-xl font-semibold mb-6">Settings</h1>

      <section className="bg-panel border border-edge rounded-xl p-5 mb-6">
        <div className="text-sm font-semibold mb-1">Library location</div>
        <p className="text-xs text-muted mb-3">
          Where your videos, thumbnails, and catalogue live. Point this at an external drive to take your vault with you.
        </p>
        <div className="flex items-center gap-2">
          <code className="flex-1 bg-ink border border-edge rounded-lg px-3 py-2 text-sm truncate">{root || "…"}</code>
          <button onClick={change} className="text-sm font-medium px-4 py-2 rounded-lg bg-edge hover:bg-panel2 shrink-0">Change folder…</button>
        </div>
        {changed && (
          <div className="mt-3 flex items-center gap-3 text-sm text-amber-400">
            Saved — restart to use the new location.
            <button onClick={() => RestartApp()} style={{ background: "var(--ac)", color: "#0a0a0a" }}
              className="font-semibold px-3 py-1.5 rounded-lg">Restart now</button>
          </div>
        )}
      </section>

      <section className="bg-panel border border-edge rounded-xl p-5">
        <div className="text-sm font-semibold mb-3">Storage</div>
        {!stats ? <div className="text-muted text-sm">Loading…</div> : (
          <>
            <div className="flex items-baseline gap-2 mb-4">
              <span className="text-3xl font-bold">{fmtSize(stats.totalBytes)}</span>
              <span className="text-muted text-sm">· {stats.videoCount} videos · {stats.modelCount} models</span>
            </div>
            <div className="space-y-3">
              {(stats.sites || []).map((s) => {
                const pct = stats.totalBytes ? Math.round((s.bytes / stats.totalBytes) * 100) : 0;
                return (
                  <div key={s.site}>
                    <div className="flex items-center justify-between text-sm mb-1">
                      <span className="flex items-center gap-2"><SourceBadge site={s.site} /><span className="text-muted">· {s.count} videos</span></span>
                      <span className="font-medium">{fmtSize(s.bytes)} <span className="text-muted text-xs">({pct}%)</span></span>
                    </div>
                    <div className="h-2 bg-ink rounded-full overflow-hidden">
                      <div className="h-full" style={{ width: pct + "%", background: siteColor(s.site) }} />
                    </div>
                  </div>
                );
              })}
            </div>
            <p className="text-xs text-muted mt-4">Per-model sizes are shown on each model card under Sources.</p>
          </>
        )}
      </section>
    </div>
  );
}

/* ---------------- Downloads ---------------- */

function ConnRow({ label, on, onConnect }: any) {
  return (
    <div className="flex items-center gap-3 py-1.5">
      <span className="w-28">{label}</span>
      <span className={`text-xs ${on ? "text-emerald-400" : "text-muted"}`}>{on ? "connected ✓" : "not connected"}</span>
      <button onClick={onConnect} className="ml-auto text-xs font-medium px-3 py-1.5 rounded-lg bg-edge hover:bg-panel2">
        {on ? "Reconnect" : "Connect"}
      </button>
    </div>
  );
}

function Downloads({ queue }: { queue: Job[] }) {
  const [url, setUrl] = useState("");
  const [status, setStatus] = useState({ x: false, pornhub: false });
  useEffect(() => { CookieStatus().then(setStatus); }, []);
  const connect = async () => setStatus(await ConnectCookies());

  const add = () => { const u = url.trim(); if (!u) return; Enqueue(u); setUrl(""); };

  return (
    <div className="p-6 max-w-3xl">
      <h1 className="text-xl font-semibold mb-4">Downloads</h1>
      <div className="flex gap-2">
        <input value={url} onChange={(e) => setUrl(e.target.value)} onKeyDown={(e) => { if (e.key === "Enter") add(); }}
          placeholder="Paste a video URL and press Enter…"
          className="flex-1 bg-panel border border-edge rounded-lg px-4 py-3 text-sm outline-none focus:border-accent" />
        <button onClick={add} className="bg-accent hover:bg-sky-500 text-white font-semibold px-6 rounded-lg">Add</button>
      </div>

      <div className="mt-6 bg-panel border border-edge rounded-lg p-4">
        <div className="text-sm font-semibold mb-1">Connections</div>
        <p className="text-xs text-muted mb-3 leading-relaxed">
          Connect an account so protected / age-restricted posts and your favorites work.
          Install <b>“Get cookies.txt LOCALLY”</b>, open <b>x.com</b> or <b>pornhub.com</b> while logged in,
          click <b>Export</b>, then connect that file here. Saved in your vault and reused automatically.
        </p>
        <ConnRow label={<SourceBadge site="PornHub" />} on={status.pornhub} onConnect={connect} />
        <ConnRow label={<SourceBadge site="Twitter" />} on={status.x} onConnect={connect} />
      </div>

      <div className="flex items-center justify-between mt-8 mb-2">
        <h2 className="font-semibold">Queue</h2>
        {queue.some((j) => ["done", "duplicate", "error"].includes(j.status)) &&
          <button onClick={() => ClearFinished()} className="text-xs text-muted hover:text-white">Clear finished</button>}
      </div>
      {queue.length === 0 ? <Empty>Nothing queued.</Empty>
        : <div className="space-y-2">{queue.map((j) => <QueueItem key={j.id} j={j} />)}</div>}
    </div>
  );
}

function QueueItem({ j }: { j: Job }) {
  return (
    <div className="bg-panel border border-edge rounded-lg p-3">
      <div className="flex items-center gap-2">
        <span className="flex-1 truncate text-sm">{j.title || j.url}</span>
        <span className={`text-[11px] px-2 py-0.5 rounded-full ${STATUS_COLORS[j.status] || "bg-edge text-muted"}`}>{j.status}</span>
        {j.status === "queued" && <button onClick={() => RemoveJob(j.id)} className="text-muted hover:text-rose-400 px-1">✕</button>}
      </div>
      {(j.status === "downloading" || j.status === "done") && (
        <div className="h-1.5 bg-ink rounded-full mt-2 overflow-hidden">
          <div className="h-full bg-accent transition-all" style={{ width: `${j.status === "done" ? 100 : j.percent || 0}%` }} />
        </div>
      )}
      <div className="text-[11px] text-muted mt-1.5">
        {j.status === "downloading" && `${(j.percent || 0).toFixed(0)}%${j.speed ? ` · ${j.speed}` : ""}${j.eta ? ` · ETA ${j.eta}` : ""}`}
        {j.status === "done" && `✓ ${j.count} file${j.count === 1 ? "" : "s"} saved`}
        {j.status === "duplicate" && "Already in your library — skipped"}
        {j.status === "error" && <span className="text-rose-400">{j.error}</span>}
      </div>
    </div>
  );
}

function Empty({ children }: { children: any }) {
  return <div className="text-muted text-sm py-16 text-center">{children}</div>;
}
