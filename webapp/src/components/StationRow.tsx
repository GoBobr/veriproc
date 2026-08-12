import { useState } from "preact/hooks";
import { ContextMenu, type ContextMenuItem } from "./ContextMenu";
import { SubmitDialog } from "./SubmitDialog";
import { PushDownstreamDialog } from "./PushDownstreamDialog";
import { useAuth } from "../state/auth";
import { taskHref, stationHref } from "../state/router";
import type { DashboardSlot, DashboardStationRow } from "../api/types";

interface Props {
  instanceID: string;
  row: DashboardStationRow;
  onChanged: () => void;
}

interface MenuState {
  x: number;
  y: number;
  items: ContextMenuItem[];
}

// StationRow renders the operational dashboard row: fixed-width label,
// horizontal slot strip, and per-interval success/failure counters.
export function StationRow({ instanceID, row, onChanged }: Props) {
  const { session, client } = useAuth();
  const [menu, setMenu] = useState<MenuState | null>(null);
  const [showSubmit, setShowSubmit] = useState(false);
  const [pushTask, setPushTask] = useState<{ taskID: string; targetStationID: string; force: boolean } | null>(null);
  const isOperator = session?.role === "operator";

  const onLabelContext = (e: MouseEvent) => {
    e.preventDefault();
    const items: ContextMenuItem[] = [];
    if (isOperator) {
      if (row.paused) {
        items.push({
          label: "Unpause station",
          onClick: () => {
            void client.unpauseStation(instanceID, row.station_id).then(onChanged);
          },
        });
      } else {
        items.push({
          label: "Pause station",
          onClick: () => {
            void client.pauseStation(instanceID, row.station_id).then(onChanged);
          },
        });
      }
      items.push({
        label: "Submit task…",
        onClick: () => setShowSubmit(true),
      });
      if (row.counts.failure > 0 || row.slots.some((slot) => slot.kind === "failed" || slot.kind === "cancelled")) {
        items.push({
          label: "Hide all failed",
          onClick: () => {
            void client.hideStationFailures(instanceID, row.station_id).then(onChanged);
          },
        });
      }
      items.push({
        label: "Show all hidden",
        onClick: () => {
          void client.unhideStation(instanceID, row.station_id).then(onChanged);
        },
      });
    } else {
      items.push({ label: "Read-only (viewer)", onClick: () => {}, disabled: true });
    }
    setMenu({ x: e.clientX, y: e.clientY, items });
  };

  const onSlotContext = (e: MouseEvent, slot: DashboardSlot) => {
    e.preventDefault();
    if (slot.kind === "empty" || !slot.task_id) return;
    const items: ContextMenuItem[] = [];
    if (!isOperator) {
      items.push({ label: "Read-only (viewer)", onClick: () => {}, disabled: true });
    } else {
      if (slot.kind === "running" || slot.kind === "queued") {
        items.push({
          label: "Cancel run",
          onClick: () => {
            void client
              .cancelRun(instanceID, slot.task_id!, slot.retry_index)
              .then(onChanged);
          },
        });
      }
      if (slot.kind === "failed" || slot.kind === "cancelled") {
        items.push({
          label: "Retry task",
          onClick: () => {
            void client.retryTask(instanceID, slot.task_id!).then(onChanged);
          },
        });
      }
      if (slot.kind === "failed" || slot.kind === "cancelled" || slot.kind === "completed") {
        items.push({
          label: "Hide from view",
          onClick: () => {
            void client
              .hideRun(instanceID, slot.task_id!, slot.retry_index, row.station_id)
              .then(onChanged);
          },
        });
      }
      // Per-downstream-station push actions derived from station definition.
      for (const targetID of (row.downstream ?? [])) {
        const tid = slot.task_id!;
        items.push({
          label: `Push to ${targetID}`,
          onClick: () => setPushTask({ taskID: tid, targetStationID: targetID, force: false }),
        });
        items.push({
          label: `Push to ${targetID} (force)`,
          onClick: () => setPushTask({ taskID: tid, targetStationID: targetID, force: true }),
        });
      }
    }
    items.push({
      label: "Open task page",
      onClick: () => {
        location.hash = taskHref(instanceID, slot.task_id!, slot.retry_index);
      },
    });
    setMenu({ x: e.clientX, y: e.clientY, items });
  };

  const onSlotClick = (slot: DashboardSlot) => {
    if (slot.kind === "empty" || !slot.task_id) return;
    window.open(taskHref(instanceID, slot.task_id, slot.retry_index), "_blank");
  };

  return (
    <div class="station-row" data-station={row.station_id} data-paused={row.paused ? "true" : undefined}>
      <div
        class="station-label"
        onContextMenu={onLabelContext}
        onClick={() => window.open(stationHref(instanceID, row.station_id), "_blank")}
        title={`${row.station_name || row.station_id} — click to open station view`}
      >
        {row.station_name || row.station_id}
        {row.paused && <span class="paused-badge">PAUSED</span>}
      </div>
      <div class="slot-strip">
        {row.slots.map((s, i) => (
          <button
            key={i}
            class="slot"
            data-kind={s.kind}
            data-task={s.task_id || ""}
            title={s.task_id ? `${s.kind}: ${s.task_id} r${s.retry_index}` : "empty"}
            onClick={() => onSlotClick(s)}
            onContextMenu={(e) => onSlotContext(e, s)}
            aria-label={s.task_id ? `${s.kind} ${s.task_id}` : "empty slot"}
          />
        ))}
        {row.overflow > 0 && <span class="slot-overflow">+{row.overflow}</span>}
      </div>
      <div class="counters">
        <span class="succ">✓ {row.counts.success}</span>{" "}
        <span class="fail">✗ {row.counts.failure}</span>
      </div>
      {menu && <ContextMenu {...menu} onClose={() => setMenu(null)} />}
      {showSubmit && (
        <SubmitDialog
          stationID={row.station_id}
          onClose={() => setShowSubmit(false)}
          onSubmit={async (body) => {
            await client.submitTask(instanceID, row.station_id, body);
            onChanged();
          }}
        />
      )}
      {pushTask && (
        <PushDownstreamDialog
          instanceID={instanceID}
          taskID={pushTask.taskID}
          targetStationID={pushTask.targetStationID}
          defaultForce={pushTask.force}
          onClose={() => setPushTask(null)}
          onSubmit={async (stationID, body) => {
            await client.submitTask(instanceID, stationID, body);
            onChanged();
          }}
        />
      )}
    </div>
  );
}
