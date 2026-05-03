export interface Agent {
  pid: number;
  sessionId: string;
  name: string;
  providerName: string;
  sessionFile: string;
  model: string;
  workingDir: string;
  status: 'Active' | 'Idle' | 'Waiting' | 'Error' | 'Unknown';
  gitBranch: string;
  tokensIn: number;
  tokensOut: number;
  estCostUSD: number;
  lastActivity: string;
  lastAction: string;
  tmuxSession: string;
  teamName: string;
  taskSubject: string;
}

export interface ToolSpan {
  name: string;
  snippet: string;
  success: boolean;
  errorMsg: string;
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
