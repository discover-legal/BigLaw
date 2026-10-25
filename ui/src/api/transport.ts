// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal
//
// The transport seam: every screen talks to BigLaw through this interface only. http.ts is the
// real API (same origin); recorded.ts replays a recorded engine in the browser with no network.
import type {
  Client, Discovery, Doc, Hit, ImportResult, ManageDetail, ManageItem, Me, NewTask, OutlineEntry, Playbook, Problem, Profile, RecordedSearch, RunEvent, SaveResult, SectionPage,
  SecretView, Setting, SpecVersion, Task, TaskCost, TestResult, TimeEntry,
} from './types.ts';

/** What the recorded engine would replay for a prompt; live transports never match. */
export interface PromptMatch {
  key: string;
  title: string;
  prompt: string;
  score: number;
  onScript: boolean;
  jurisdiction?: string;
}

export interface Transport {
  readonly mode: 'live' | 'recorded';
  /** Shown in the demo banner (recorded engine only). */
  readonly provenance: string | null;
  /** The site the recorded demo is embedded in (recorded engine only): the banner links back to it. */
  readonly home?: string;
  clients(): Promise<Client[]>;
  documents(matterNumber?: string): Promise<Doc[]>;
  outline(docId: string): Promise<OutlineEntry[]>;
  section(docId: string, sectionId: string, offset?: number): Promise<SectionPage>;
  /** Where the document's original file opens (a scan, to check its transcript against); null: none. */
  fileUrl(docId: string): string | null;
  search(q: string, ids: string[]): Promise<Hit[]>;
  tasks(): Promise<Task[]>;
  task(id: string): Promise<Task>;
  match(description: string, matterNumber: string): PromptMatch | null;
  submit(t: NewTask, replayKey?: string): Promise<Task>;
  cancel(id: string): Promise<Task>;
  /** Approve, or reject with a reason (required: the lead is told what to fix). */
  decideGate(id: string, gateId: string, approve: boolean, reason?: string): Promise<Task>;
  cost(id: string): Promise<TaskCost>;
  /** Streams a task's steps; returns the unsubscribe function. */
  events(id: string, on: (e: RunEvent) => void): () => void;
  /** Recorded engine only: the matter's recorded tasks, offered as prompts. */
  suggestions?(matterNumber: string): PromptMatch[];
  /** Recorded engine only: finish the replay now. */
  fastForward?(id: string): void;
  /** Recorded engine only: searches whose hits v2's own Search computed when the run was recorded. */
  recordedSearches?(matterNumber: string): RecordedSearch[];
  /** Recorded engine only: this build's configuration defaults (read-only). */
  settings?(): Promise<Setting[]>;
  playbooks(): Promise<Playbook[]>;
  timeEntries(): Promise<TimeEntry[]>;
  profiles(): Promise<Profile[]>;
  me(): Promise<Me>;
  /** Manage: the catalogue, an item with its contents, its versions (each with its files), the change log. */
  manageItems(): Promise<ManageItem[]>;
  manageItem(id: string): Promise<ManageDetail>;
  manageHistory(id: string): Promise<SpecVersion[]>;
  manageChanges(): Promise<SpecVersion[]>;
  /** Admin only. base: the item's latest version when it was loaded (a stale one is a 409). */
  manageSave(id: string, base: number, files: Record<string, string>, note: string): Promise<SaveResult>;
  manageRevert(id: string, to: number, base: number, note: string): Promise<SaveResult>;
  manageDelete(id: string, base: number, note: string): Promise<SaveResult>;
  /** Custom tools: secrets by name (never a value), an MCP server's tools and their import, and a Test call (admin). */
  manageSecrets(): Promise<SecretView[]>;
  /** origin: the https URL (scheme, host, port) of the only server the secret is ever sent to. */
  manageSetSecret(name: string, value: string, origin: string): Promise<void>;
  manageDeleteSecret(name: string): Promise<void>;
  manageDiscover(id: string): Promise<Discovery>;
  manageImport(id: string, tools: string[], offerToLead: boolean): Promise<ImportResult[]>;
  manageTest(id: string, documentIds: string[], input: unknown): Promise<TestResult>;
  /** Where the overrides download (all, or one item's); null: the recorded engine has none. */
  exportUrl(format: 'patch' | 'zip', item?: string): string | null;
}

/** A refusal carries BigLaw's reason and what to do next (README: actionable errors). */
export class ApiError extends Error {
  readonly status: number;
  /** A refused Manage save: every problem, by file. */
  readonly problems: Problem[];
  constructor(status: number, message: string, problems: Problem[] = []) {
    super(message);
    this.status = status;
    this.problems = problems;
  }
}
