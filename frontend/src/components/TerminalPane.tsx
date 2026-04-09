import { useEffect, useRef, useCallback, useState } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { SearchAddon } from "@xterm/addon-search";
import "@xterm/xterm/css/xterm.css";

// RPC helper (same as App.tsx)
async function rpc(method: string, ...args: unknown[]): Promise<any> {
  try {
    return await (window as any).wails.Call.ByName(
      `main.NexusService.${method}`,
      ...args
    );
  } catch {
    return null;
  }
}

interface MCPServer {
  name: string;
  url: string;
  connected: boolean;
}

interface Props {
  sessionId: string;
  agentType: string;
  onWrite: (data: string) => void;
  onResize: (cols: number, rows: number) => void;
}

export function TerminalPane({ sessionId, agentType, onWrite, onResize }: Props) {
  const containerRef = useRef<HTMLDivElement>(null);
  const termRef = useRef<Terminal | null>(null);
  const fitRef = useRef<FitAddon | null>(null);
  const searchRef = useRef<SearchAddon | null>(null);
  const [showSearch, setShowSearch] = useState(false);
  const [searchText, setSearchText] = useState("");
  const [mcpServers, setMcpServers] = useState<MCPServer[]>([]);

  // Poll MCP servers for non-shell sessions
  useEffect(() => {
    if (agentType === "shell") return;
    const refresh = async () => {
      const ms = await rpc("GetMCPServers");
      if (Array.isArray(ms)) setMcpServers(ms);
    };
    refresh();
    const iv = setInterval(refresh, 5000);
    return () => clearInterval(iv);
  }, [agentType]);

  // Initialize xterm.js — synchronous creation, async theme update
  useEffect(() => {
    if (!containerRef.current) return;

    const term = new Terminal({
      cursorBlink: true,
      fontSize: 14,
      fontFamily: "'Cascadia Code', 'Consolas', 'Courier New', monospace",
      theme: {
        background: "#0a0a0f",
        foreground: "#e5e5e5",
        cursor: "#60a5fa",
        selectionBackground: "#3b82f633",
      },
      scrollback: 10000,
    });

    const fitAddon = new FitAddon();
    const searchAddon = new SearchAddon();
    term.loadAddon(fitAddon);
    term.loadAddon(searchAddon);
    term.open(containerRef.current);
    termRef.current = term;
    fitRef.current = fitAddon;
    searchRef.current = searchAddon;

    requestAnimationFrame(() => {
      try { fitAddon.fit(); } catch (e) { console.debug("fit failed", e); }
    });

    // Async: load custom theme from backend config and apply
    rpc("GetThemeConfig").then((cfg: any) => {
      if (!cfg) return;
      const t: Record<string, string> = {};
      if (cfg.background) t.background = cfg.background;
      if (cfg.foreground) t.foreground = cfg.foreground;
      if (cfg.cursor) t.cursor = cfg.cursor;
      if (Object.keys(t).length > 0) term.options.theme = { ...term.options.theme, ...t };
      if (cfg.font_family) term.options.fontFamily = cfg.font_family;
      if (cfg.font_size && cfg.font_size > 0) term.options.fontSize = cfg.font_size;
      try { fitAddon.fit(); } catch (e) { console.debug("fit after theme", e); }
    });

    return () => {
      term.dispose();
      termRef.current = null;
      fitRef.current = null;
      searchRef.current = null;
    };
  }, []);

  // Fix IME composition window position (Windows WebView2 + flexlayout).
  // The hidden textarea xterm uses for input can lose sync with the visible
  // cursor when nested inside positioned/overflow containers.
  // Key insight: Windows reads textarea position BEFORE the JS compositionstart
  // event fires, so we must keep the textarea positioned proactively.
  useEffect(() => {
    const el = containerRef.current;
    const term = termRef.current;
    if (!el || !term) return;

    const textarea = el.querySelector(
      ".xterm-helper-textarea"
    ) as HTMLTextAreaElement | null;
    if (!textarea) return;

    const syncIMEPosition = () => {
      const core = (term as any)._core;
      const dims = core?._renderService?.dimensions?.css?.cell;
      if (!dims) return;
      const buf = term.buffer.active;
      textarea.style.left = `${buf.cursorX * dims.width}px`;
      textarea.style.top = `${buf.cursorY * dims.height}px`;
      textarea.style.width = `${dims.width}px`;
      textarea.style.height = `${dims.height}px`;
      textarea.style.lineHeight = `${dims.height}px`;
      textarea.style.fontSize = `${term.options.fontSize}px`;
    };

    // keydown + capture: fires before IME reads textarea position
    textarea.addEventListener("keydown", syncIMEPosition, { capture: true });
    textarea.addEventListener("focus", syncIMEPosition);
    textarea.addEventListener("compositionstart", syncIMEPosition);
    // Sync after terminal output moves cursor
    const writeSub = term.onWriteParsed(syncIMEPosition);

    return () => {
      textarea.removeEventListener("keydown", syncIMEPosition, { capture: true });
      textarea.removeEventListener("focus", syncIMEPosition);
      textarea.removeEventListener("compositionstart", syncIMEPosition);
      writeSub.dispose();
    };
  }, []);

  // Forward keyboard input to Go backend
  useEffect(() => {
    const term = termRef.current;
    if (!term) return;

    // Ctrl+Shift+F: toggle terminal search
    // Ctrl+C: copy if selection exists, else send ^C
    term.attachCustomKeyEventHandler((ev: KeyboardEvent) => {
      if (ev.type === "keydown" && ev.ctrlKey && ev.shiftKey && ev.key === "F") {
        setShowSearch((prev) => !prev);
        return false;
      }
      if (ev.type === "keydown" && ev.key === "Escape" && showSearch) {
        setShowSearch(false);
        searchRef.current?.clearDecorations();
        return false;
      }
      if (ev.type === "keydown" && ev.ctrlKey && ev.key === "c") {
        const sel = term.getSelection();
        if (sel) {
          navigator.clipboard.writeText(sel).catch(() => {});
          term.clearSelection();
          return false;
        }
      }
      // Ctrl+V: paste from clipboard
      if (ev.type === "keydown" && ev.ctrlKey && ev.key === "v") {
        ev.preventDefault(); // prevent browser paste → xterm onData duplicate
        navigator.clipboard
          .readText()
          .then((text) => {
            if (text) onWrite(text);
          })
          .catch(() => {});
        return false;
      }
      return true;
    });

    const dispose = term.onData((data: string) => {
      onWrite(data);
    });

    return () => dispose.dispose();
  }, [onWrite]);

  // Subscribe to terminal output events from Go backend.
  // Uses AbortController pattern to guarantee cleanup even if
  // wails.Events.On returns an unexpected value.
  useEffect(() => {
    const wails = (window as any).wails;
    if (!wails?.Events?.On) return;

    let cancelled = false;
    let unsubFn: (() => void) | null = null;

    const result = wails.Events.On(
      "nexus:terminal:output",
      (ev: { data: any }) => {
        if (cancelled) return;
        const payload = ev.data;
        if (!payload || payload.session_id !== sessionId) return;

        if (payload.type === "exit") {
          termRef.current?.writeln(
            "\r\n\x1b[90m[Process exited]\x1b[0m"
          );
        } else if (payload.data) {
          termRef.current?.write(payload.data);
        }
      }
    );

    // Handle both function return and call-id return from wails.Events.On.
    if (typeof result === "function") {
      unsubFn = result;
    } else if (result && typeof result.cancel === "function") {
      unsubFn = () => result.cancel();
    } else if (result && typeof wails.Events.Off === "function") {
      // Fallback: use Off with the event name (less precise but prevents leaks).
      unsubFn = () => {
        try { wails.Events.Off("nexus:terminal:output"); } catch {}
      };
    }

    return () => {
      cancelled = true;
      if (unsubFn) {
        try { unsubFn(); } catch {}
      }
    };
  }, [sessionId]);

  // Handle resize — debounced to prevent CJK text jitter during rapid resizes.
  const resizeTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const handleResize = useCallback(() => {
    // Debounce: cancel pending resize, schedule new one.
    if (resizeTimerRef.current) {
      clearTimeout(resizeTimerRef.current);
    }
    resizeTimerRef.current = setTimeout(() => {
      const fit = fitRef.current;
      const term = termRef.current;
      const el = containerRef.current;
      if (!fit || !term || !el) return;
      if (el.clientWidth < 10 || el.clientHeight < 10) return;
      try {
        fit.fit();
        onResize(term.cols, term.rows);
      } catch (e) { console.debug("fit failed", e); }
    }, 50); // 50ms debounce
  }, [onResize]);

  useEffect(() => {
    window.addEventListener("resize", handleResize);
    const el = containerRef.current;
    if (!el) return;

    // ResizeObserver for splitter drag
    const resizeObs = new ResizeObserver(() => handleResize());
    resizeObs.observe(el);

    // IntersectionObserver for tab visibility (flexlayout hides inactive tabs)
    const visObs = new IntersectionObserver((entries) => {
      if (entries[0]?.isIntersecting) {
        requestAnimationFrame(() => handleResize());
      }
    });
    visObs.observe(el);

    return () => {
      window.removeEventListener("resize", handleResize);
      resizeObs.disconnect();
      visObs.disconnect();
      if (resizeTimerRef.current) {
        clearTimeout(resizeTimerRef.current);
      }
    };
  }, [handleResize]);

  // Right-click context menu
  const [ctxMenu, setCtxMenu] = useState<{ x: number; y: number } | null>(null);

  useEffect(() => {
    const el = containerRef.current;
    if (!el) return;

    const onContextMenu = (e: MouseEvent) => {
      e.preventDefault();
      setCtxMenu({ x: e.clientX, y: e.clientY });
    };
    el.addEventListener("contextmenu", onContextMenu);

    const onClickAway = () => setCtxMenu(null);
    document.addEventListener("click", onClickAway);

    return () => {
      el.removeEventListener("contextmenu", onContextMenu);
      document.removeEventListener("click", onClickAway);
    };
  }, []);

  const ctxCopy = useCallback(() => {
    const sel = termRef.current?.getSelection();
    if (sel) navigator.clipboard.writeText(sel).catch(() => {});
    setCtxMenu(null);
  }, []);

  const ctxPaste = useCallback(() => {
    navigator.clipboard.readText().then((text) => {
      if (text) onWrite(text);
    }).catch(() => {});
    setCtxMenu(null);
  }, [onWrite]);

  const ctxClear = useCallback(() => {
    termRef.current?.clear();
    setCtxMenu(null);
  }, []);

  const ctxSelectAll = useCallback(() => {
    termRef.current?.selectAll();
    setCtxMenu(null);
  }, []);

  // Search handlers
  const doSearch = useCallback((text: string) => {
    if (searchRef.current && text) {
      searchRef.current.findNext(text);
    }
  }, []);

  const doSearchPrev = useCallback(() => {
    if (searchRef.current && searchText) {
      searchRef.current.findPrevious(searchText);
    }
  }, [searchText]);

  return (
    <div style={{ display: "flex", flexDirection: "column", width: "100%", height: "100%" }}>
      <div
        ref={containerRef}
        style={{
          flex: 1,
          contain: "strict",
          willChange: "contents",
          position: "relative",
        }}
      >
        {/* Terminal search bar (Ctrl+Shift+F) */}
        {showSearch && (
          <div className="nexus-search-bar">
            <input
              autoFocus
              placeholder="Search terminal..."
              value={searchText}
              onChange={(e) => {
                setSearchText(e.target.value);
                doSearch(e.target.value);
              }}
              onKeyDown={(e) => {
                if (e.key === "Enter") {
                  if (e.shiftKey) doSearchPrev();
                  else doSearch(searchText);
                }
                if (e.key === "Escape") {
                  setShowSearch(false);
                  searchRef.current?.clearDecorations();
                }
              }}
            />
            <button onClick={() => { setShowSearch(false); searchRef.current?.clearDecorations(); }}>x</button>
          </div>
        )}

        {/* Right-click context menu */}
        {ctxMenu && (
          <div
            className="nexus-ctx-menu"
            style={{ left: ctxMenu.x, top: ctxMenu.y }}
          >
            <button onClick={ctxCopy}>Copy</button>
            <button onClick={ctxPaste}>Paste</button>
            <button onClick={ctxSelectAll}>Select All</button>
            <hr />
            <button onClick={() => { setShowSearch(true); setCtxMenu(null); }}>Search (Ctrl+Shift+F)</button>
            <button onClick={ctxClear}>Clear</button>
          </div>
        )}
      </div>

      {/* MCP Status bar — only for agent sessions */}
      {agentType !== "shell" && mcpServers.length > 0 && (
        <div className="nexus-statusbar">
          {mcpServers.map((m) => (
            <span key={m.name} className="nexus-sb-mcp">
              <span className={`sb-dot ${m.connected ? "ok" : "warn"}`} />
              {m.name}
            </span>
          ))}
          <span className="nexus-sb-spacer" />
          <span className="nexus-sb-mcp" style={{ color: "#555" }}>
            {agentType} · {sessionId}
          </span>
        </div>
      )}
    </div>
  );
}
