export interface Agent {
  PID: number;
  SessionID: string;
  Name: string;
  ProviderName: string;
  SessionFile: string;
  Model: string;
  WorkingDir: string;
  Status: number; // 0=Active, 1=Idle, 2=WaitingPermission, 3=Error, 4=Unknown
  GitBranch: string;
  TokensIn: number;
  TokensOut: number;
  EstCostUSD: number;
  LastActivity: string;
  LastAction: string;
  TMuxSession: string;
  TeamName: string;
  TaskSubject: string;
  Title: string;
}

export const StatusLabel: Record<number, string> = {
  0: 'Active',
  1: 'Idle',
  2: 'Waiting',
  3: 'Error',
  4: 'Unknown',
};

export interface ToolSpan {
  name: string;
  snippet: string;
  success: boolean;
  errorMsg: string;
  filePath?: string;
  oldString?: string;
  newString?: string;
  command?: string;
  description?: string;
  content?: string;
  pattern?: string;
  searchPath?: string;
  prompt?: string;
}

export interface Turn {
  number: number;
  timestamp: string;
  userText: string;
  outputText: string;
  actions: ToolSpan[];
  tokensIn: number;
  tokensOut: number;
  costUSD: number;
  model: string;
}
