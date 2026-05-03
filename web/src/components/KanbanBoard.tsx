import { useState } from 'react';
import {
  DndContext,
  DragOverlay,
  closestCenter,
  PointerSensor,
  useSensor,
  useSensors,
  type DragEndEvent,
  type DragStartEvent,
} from '@dnd-kit/core';
import { SortableContext, useSortable, verticalListSortingStrategy } from '@dnd-kit/sortable';
import { CSS } from '@dnd-kit/utilities';
import type { Agent } from '../types';
import { AgentCard } from './AgentCard';

interface Props {
  agents: Agent[];
  viewMode: 'status' | 'repo';
  selectedId: string | null;
  onSelect: (id: string) => void;
}

interface Column {
  id: string;
  label: string;
  dotColor: string;
  agents: Agent[];
}

export function KanbanBoard({ agents, viewMode, selectedId, onSelect }: Props) {
  const [activeId, setActiveId] = useState<string | null>(null);

  const sensors = useSensors(
    useSensor(PointerSensor, {
      activationConstraint: {
        distance: 8,
      },
    })
  );

  const columns: Column[] = viewMode === 'status'
    ? [
        {
          id: 'Active',
          label: 'Active',
          dotColor: '#69DF73',
          agents: agents.filter(a => a.status === 'Active'),
        },
        {
          id: 'Idle',
          label: 'Idle',
          dotColor: '#666666',
          agents: agents.filter(a => a.status === 'Idle'),
        },
        {
          id: 'Waiting',
          label: 'Waiting',
          dotColor: '#FFB251',
          agents: agents.filter(a => a.status === 'Waiting'),
        },
        {
          id: 'Error',
          label: 'Error',
          dotColor: '#FF3131',
          agents: agents.filter(a => a.status === 'Error'),
        },
      ]
    : Array.from(new Set(agents.map(a => a.name))).map(repoName => ({
        id: repoName,
        label: repoName,
        dotColor: '#49D3B4',
        agents: agents.filter(a => a.name === repoName),
      }));

  const handleDragStart = (event: DragStartEvent) => {
    setActiveId(event.active.id as string);
  };

  const handleDragEnd = (event: DragEndEvent) => {
    const { active, over } = event;
    setActiveId(null);

    if (!over || active.id === over.id) return;

    console.log('[KanbanBoard] drag-end:', {
      agentId: active.id,
      fromContainer: active.data.current?.sortable?.containerId,
      toContainer: over.id,
    });

    // Backend mutation will come in a later task
  };

  const activeAgent = activeId ? agents.find(a => (a.sessionId || a.pid.toString()) === activeId) : null;

  return (
    <DndContext
      sensors={sensors}
      collisionDetection={closestCenter}
      onDragStart={handleDragStart}
      onDragEnd={handleDragEnd}
    >
      <div style={{
        display: 'flex',
        gap: 12,
        padding: '14px 18px',
        overflowX: 'auto',
        flex: 1,
      }}>
        {columns.map(column => (
          <KanbanColumn
            key={column.id}
            column={column}
            selectedId={selectedId}
            onSelect={onSelect}
          />
        ))}
      </div>
      <DragOverlay>
        {activeAgent ? (
          <div style={{ opacity: 0.8 }}>
            <AgentCard agent={activeAgent} selected={false} onClick={() => {}} />
          </div>
        ) : null}
      </DragOverlay>
    </DndContext>
  );
}

interface ColumnProps {
  column: Column;
  selectedId: string | null;
  onSelect: (id: string) => void;
}

function KanbanColumn({ column, selectedId, onSelect }: ColumnProps) {
  const agentIds = column.agents.map(a => a.sessionId || a.pid.toString());

  return (
    <div style={{
      minWidth: 250,
      flex: 1,
      background: 'var(--bg-1)',
      borderRadius: 6,
      border: '1px solid var(--border)',
      display: 'flex',
      flexDirection: 'column' as const,
      maxHeight: '100%',
    }}>
      {/* Header */}
      <div style={{
        padding: '10px 12px',
        borderBottom: '1px solid var(--border)',
        display: 'flex',
        alignItems: 'center',
        gap: 8,
        flexShrink: 0,
      }}>
        <div style={{
          width: 8,
          height: 8,
          borderRadius: '50%',
          background: column.dotColor,
          flexShrink: 0,
        }} />
        <span style={{
          fontSize: 11,
          textTransform: 'uppercase' as const,
          letterSpacing: '0.06em',
          color: 'var(--fg-3)',
          fontWeight: 600,
          flex: 1,
        }}>
          {column.label}
        </span>
        <span style={{
          fontSize: 11,
          fontWeight: 700,
          color: 'var(--fg-2)',
          background: 'var(--bg-3)',
          padding: '2px 6px',
          borderRadius: 3,
        }}>
          {column.agents.length}
        </span>
      </div>

      {/* Body */}
      <SortableContext items={agentIds} strategy={verticalListSortingStrategy}>
        <div style={{
          padding: 8,
          display: 'flex',
          flexDirection: 'column' as const,
          gap: 8,
          overflowY: 'auto',
          flex: 1,
        }}>
          {column.agents.map(agent => (
            <SortableAgentCard
              key={agent.sessionId || agent.pid}
              agent={agent}
              selected={selectedId === (agent.sessionId || agent.pid.toString())}
              onSelect={onSelect}
            />
          ))}
        </div>
      </SortableContext>
    </div>
  );
}

interface SortableAgentCardProps {
  agent: Agent;
  selected: boolean;
  onSelect: (id: string) => void;
}

function SortableAgentCard({ agent, selected, onSelect }: SortableAgentCardProps) {
  const id = agent.sessionId || agent.pid.toString();
  const {
    attributes,
    listeners,
    setNodeRef,
    transform,
    transition,
    isDragging,
  } = useSortable({ id });

  const style = {
    transform: CSS.Transform.toString(transform),
    transition,
    opacity: isDragging ? 0.5 : 1,
  };

  return (
    <div ref={setNodeRef} style={style} {...attributes} {...listeners}>
      <AgentCard agent={agent} selected={selected} onClick={() => onSelect(id)} />
    </div>
  );
}
