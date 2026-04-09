import { useRef, useCallback, useEffect, useState } from "react";
import {
  Layout,
  Model,
  TabNode,
  Actions,
  DockLocation,
} from "flexlayout-react";
import type { IJsonModel } from "flexlayout-react";
import "flexlayout-react/style/dark.css";
import "./styles/flexlayout-dark.css";
import { TerminalPane } from "./components/TerminalPane";

// ═══ Wails RPC ═══

async function rpc(method: string, ...args: unknown[]): Promise<any> {
  try {
    return await (window as any).wails.Call.ByName(
      `main.NexusService.${method}`,
      ...args
    );
  } catch {
    console.warn(`RPC ${method} failed`);
    return null;
  }
}

// ═══ Layout persistence ═══

import { defaultLayout } from "./lib/defaultLayout";

const STORAGE_KEY = "nexus-flex-layout";
const LAYOUT_VERSION_KEY = "nexus-flex-layout-ver";
const LAYOUT_VERSION = 1;

function loadLayout(): IJsonModel | null {
  if (localStorage.getItem(LAYOUT_VERSION_KEY) !== String(LAYOUT_VERSION)) {
    localStorage.removeItem(STORAGE_KEY);
    localStorage.setItem(LAYOUT_VERSION_KEY, String(LAYOUT_VERSION));
    return null;
  }
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    return raw ? JSON.parse(raw) : null;
  } catch {
    return null;
  }
}

function saveLayout(model: Model) {
  localStorage.setItem(STORAGE_KEY, JSON.stringify(model.toJson()));
  localStorage.setItem(LAYOUT_VERSION_KEY, String(LAYOUT_VERSION));
}

// ═══ Agent config ═══

interface AgentBtn {
  type: string;
  label: string;
  cls: string;
}

const AGENTS: AgentBtn[] = [
  { type: "shell", label: "+Sh", cls: "" },
  { type: "claude", label: "+C", cls: "btn-claude" },
  { type: "codex", label: "+X", cls: "btn-codex" },
  { type: "gemini", label: "+G", cls: "btn-gemini" },
];

const AGENT_COLORS: Record<string, string> = {
  claude: "#f59e0b",
  codex: "#10b981",
  gemini: "#818cf8",
  shell: "#60a5fa",
};

const AGENT_LABELS: Record<string, string> = {
  claude: "Claude",
  codex: "Codex",
  gemini: "Gemini",
  shell: "Shell",
};

// ═══ App ═══

export default function App() {
  const layoutRef = useRef<Layout | null>(null);
  const [status, setStatus] = useState("connecting...");
  const [loadingSessions, setLoadingSessions] = useState<Set<string>>(new Set());
  const [sessionCount, setSessionCount] = useState(0);

  const [model] = useState<Model>(() => {
    const saved = loadLayout();
    try {
      return Model.fromJson(saved || defaultLayout);
    } catch {
      localStorage.removeItem(STORAGE_KEY);
      return Model.fromJson(defaultLayout);
    }
  });

  // Health check with auto-reconnect
  const [disconnected, setDisconnected] = useState(false);
  useEffect(() => {
    const check = async () => {
      const r = await rpc("Ping");
      if (typeof r === "string") {
        setStatus(r);
        setDisconnected(false);
        const ss = await rpc("ListTerminals");
        if (Array.isArray(ss)) setSessionCount(ss.length);
      } else {
        setDisconnected(true);
      }
    };
    check();
    const id = setInterval(check, 3000);
    return () => clearInterval(id);
  }, []);

  // On startup: restore PTY sessions for tabs restored from localStorage,
  // or create a default shell tab if layout is empty.
  useEffect(() => {
    if (status !== "ok") return;

    const terminalTabs: { id: string; agentType: string; name: string }[] = [];
    model.visitNodes((node: any) => {
      if (node.getType() === "tab" && node.getComponent() === "terminal") {
        const cfg = node.getConfig() as { sessionId?: string; agentType?: string } | undefined;
        if (cfg?.sessionId) {
          terminalTabs.push({
            id: cfg.sessionId,
            agentType: cfg.agentType || "shell",
            name: node.getName() || cfg.sessionId,
          });
        }
      }
    });

    if (terminalTabs.length > 0) {
      terminalTabs.forEach((t) => {
        rpc("RestoreTerminal", t.id, t.agentType, t.name);
      });
    } else {
      addTerminal("shell");
    }
  }, [status]);

  // ═══ Terminal lifecycle ═══

  const findTargetTabset = useCallback((): string => {
    const active = model.getActiveTabset();
    if (active) return active.getId();
    let found = "";
    model.visitNodes((node: any) => {
      if (!found && node.getType() === "tabset") {
        found = node.getId();
      }
    });
    return found || "main";
  }, [model]);

  const addTerminal = useCallback(
    async (agentType: string) => {
      // Show loading state for non-shell agents.
      const loadingId = `loading-${Date.now()}`;
      if (agentType !== "shell") {
        setLoadingSessions((prev) => new Set(prev).add(loadingId));
      }

      let id: string | null = null;
      try {
        if (agentType === "shell") {
          id = await rpc("CreateTerminal", "");
        } else {
          id = await rpc("CreateAgentTerminal", agentType, "", "", "");
        }
      } finally {
        setLoadingSessions((prev) => {
          const next = new Set(prev);
          next.delete(loadingId);
          return next;
        });
      }

      if (typeof id !== "string") {
        console.error("Failed to create terminal");
        return;
      }

      const name =
        agentType === "shell"
          ? id
          : `${agentType}-${id.split("-").pop()}`;

      const target = findTargetTabset();
      model.doAction(
        Actions.addNode(
          {
            type: "tab",
            id: id,
            component: "terminal",
            name: name,
            enableRenderOnDemand: false,
            config: { sessionId: id, agentType },
          },
          target,
          DockLocation.CENTER,
          -1,
          true
        )
      );
    },
    [model, findTargetTabset]
  );

  // Listen for sessions created by MCP tools (agent calling create_session).
  useEffect(() => {
    const wails = (window as any).wails;
    if (!wails?.Events?.On) return;
    const unsub = wails.Events.On(
      "nexus:session:created",
      (ev: { data: any }) => {
        const d = ev.data;
        if (!d?.session_id) return;
        const id = d.session_id as string;
        const agentType = (d.agent_type as string) || "shell";
        const name = (d.name as string) || (agentType === "shell" ? id : `${agentType}-${id.split("-").pop()}`);
        if (model.getNodeById(id)) return;
        const target = findTargetTabset();
        model.doAction(
          Actions.addNode(
            {
              type: "tab",
              id,
              component: "terminal",
              name,
              enableRenderOnDemand: false,
              config: { sessionId: id, agentType },
            },
            target,
            DockLocation.CENTER,
            -1,
            true
          )
        );
      }
    );
    return () => { if (typeof unsub === "function") unsub(); };
  }, [model, findTargetTabset]);

  // ═══ Session destroyed → remove tab ═══

  useEffect(() => {
    const wails = (window as any).wails;
    if (!wails?.Events?.On) return;
    const unsub = wails.Events.On(
      "nexus:session:destroyed",
      (ev: { data: any }) => {
        const id = ev.data?.session_id as string;
        if (!id) return;
        const node = model.getNodeById(id);
        if (node) {
          model.doAction(Actions.deleteTab(id));
        }
      }
    );
    return () => { if (typeof unsub === "function") unsub(); };
  }, [model]);

  // ═══ Tab factory ═══

  const factory = useCallback((node: TabNode) => {
    const component = node.getComponent();
    if (component === "terminal") {
      const cfg = node.getConfig() as {
        sessionId: string;
        agentType: string;
      };
      return (
        <TerminalPane
          sessionId={cfg.sessionId}
          agentType={cfg.agentType || "shell"}
          onWrite={(data) => rpc("WriteTerminal", cfg.sessionId, data)}
          onResize={(cols, rows) =>
            rpc("ResizeTerminal", cfg.sessionId, cols, rows)
          }
        />
      );
    }
    return <div style={{ padding: 20, color: "#666" }}>Unknown: {component}</div>;
  }, []);

  // ═══ Tab close → confirm for agents, then destroy PTY ═══

  const handleAction = useCallback(
    (action: any) => {
      if (action.type === "FlexLayout_DeleteTab") {
        const tabId = action.data?.node;
        if (tabId) {
          // Check if it's an agent tab (non-shell) — show confirmation.
          const node = model.getNodeById(tabId);
          if (node) {
            const cfg = (node as TabNode).getConfig?.() as { agentType?: string } | undefined;
            const agentType = cfg?.agentType || "shell";
            if (agentType !== "shell") {
              const label = AGENT_LABELS[agentType] || agentType;
              if (!window.confirm(`Close ${label} session "${tabId}"? The agent process will be terminated.`)) {
                return undefined; // cancel the action
              }
            }
          }
          rpc("DestroyTerminal", tabId);
        }
      }
      return action;
    },
    [model]
  );

  // ═══ Persist layout ═══

  const handleModelChange = useCallback(() => {
    saveLayout(model);
  }, [model]);

  // ═══ Custom tab rendering (agent icon + color) ═══

  const renderTabSet = useCallback(
    (_node: any, _renderValues: any) => {},
    []
  );

  const onRenderTab = useCallback(
    (node: TabNode, renderValues: { leading: any; content: any }) => {
      const cfg = node.getConfig() as { agentType?: string } | undefined;
      const agentType = cfg?.agentType || "shell";
      const color = AGENT_COLORS[agentType] || "#666";
      const icon =
        agentType === "claude"
          ? "C"
          : agentType === "codex"
            ? "X"
            : agentType === "gemini"
              ? "G"
              : ">_";
      renderValues.leading = (
        <span style={{ color, fontWeight: 700, fontSize: 10, marginRight: 4 }}>
          {icon}
        </span>
      );
    },
    []
  );

  return (
    <div className="nexus-root">
      <div className="nexus-toolbar">
        <span className="nexus-title">Nexus</span>
        <div className="nexus-toolbar-btns">
          {AGENTS.map((a) => (
            <button
              key={a.type}
              className={`nexus-add-btn ${a.cls}`}
              onClick={() => addTerminal(a.type)}
              title={`New ${a.type}`}
            >
              {a.label}
            </button>
          ))}
        </div>
        {/* Loading indicator for agent creation */}
        {loadingSessions.size > 0 && (
          <div className="nexus-loading">
            <span className="nexus-spinner" />
            <span style={{ marginLeft: 6, fontSize: 12, color: "#888" }}>
              Starting agent...
            </span>
          </div>
        )}
        <div className="nexus-info">
          <span className={`dot ${status === "ok" ? "" : "disconnected"}`} />
          <span>MCP :9400</span>
          <span>{sessionCount} session{sessionCount !== 1 ? "s" : ""}</span>
        </div>
      </div>
      {disconnected && (
        <div className="nexus-toast">Backend disconnected — reconnecting...</div>
      )}
      <div className="nexus-layout">
        <Layout
          ref={layoutRef}
          model={model}
          factory={factory}
          onAction={handleAction}
          onModelChange={handleModelChange}
          onRenderTabSet={renderTabSet}
          onRenderTab={onRenderTab}
        />
      </div>
    </div>
  );
}
