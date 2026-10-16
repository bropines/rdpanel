import { useQueryClient } from "@tanstack/react-query";
import { createContext, useContext, useEffect, useState, type ReactNode } from "react";

import type { Schemas } from "@/api/client";
import { deviceKeys } from "@/api/devices";
import { connectEvents, type StreamStatus } from "@/api/events";
import { applyPresence, mergeDevice, patchDevicePage } from "@/lib/live-cache";

type Device = Schemas["Device"];
type DevicePage = Schemas["DevicePage"];

const LiveContext = createContext<StreamStatus>("closed");

/** Keeps one SSE connection for the authenticated app and patches the query cache. */
export function LiveProvider({ children }: { children: ReactNode }) {
  const qc = useQueryClient();
  const [status, setStatus] = useState<StreamStatus>("connecting");

  useEffect(() => {
    if (typeof EventSource === "undefined") {
      setStatus("closed");
      return;
    }
    return connectEvents({
      onStatus: setStatus,
      onReconnect: () => {
        void qc.invalidateQueries({ queryKey: deviceKeys.all });
        void qc.invalidateQueries({ queryKey: ["dashboard"] });
      },
      onEvent: (name, data) => {
        switch (name) {
          case "presence.changed": {
            const ev = data as Schemas["PresenceChangedEvent"];
            qc.setQueriesData<DevicePage>({ queryKey: ["devices", "list"] }, (p) =>
              patchDevicePage(p, ev.id, (d) => applyPresence(d, ev)),
            );
            qc.setQueryData<Schemas["DeviceDetail"]>(deviceKeys.detail(ev.id), (d) => (d ? applyPresence(d, ev) as Schemas["DeviceDetail"] : d));
            qc.setQueryData<Schemas["Dashboard"]>(["dashboard"], (d) =>
              d ? { ...d, devices_online: Math.max(0, d.devices_online + (ev.online ? 1 : -1)) } : d,
            );
            break;
          }
          case "device.updated": {
            const dev = data as Device;
            qc.setQueriesData<DevicePage>({ queryKey: ["devices", "list"] }, (p) =>
              patchDevicePage(p, dev.id, (d) => mergeDevice(d, dev)),
            );
            qc.setQueryData<Schemas["DeviceDetail"]>(deviceKeys.detail(dev.id), (d) => (d ? mergeDevice(d, dev) : d));
            break;
          }
          case "audit.connection":
            void qc.invalidateQueries({ queryKey: ["audit", "connections"] });
            void qc.invalidateQueries({ queryKey: ["dashboard"] });
            break;
          case "hbbs.health":
            qc.setQueryData(["server", "health"], data);
            break;
          case "relay.health":
            void qc.invalidateQueries({ queryKey: ["relays"] });
            break;
        }
      },
    });
  }, [qc]);

  return <LiveContext.Provider value={status}>{children}</LiveContext.Provider>;
}

// eslint-disable-next-line react-refresh/only-export-components
export function useLiveStatus(): StreamStatus {
  return useContext(LiveContext);
}
