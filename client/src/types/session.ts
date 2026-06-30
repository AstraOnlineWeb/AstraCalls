export type SessionState = "connecting" | "qr" | "open" | "logged_out";

export type SessionInfo = {
  id: string;
  name: string;
  jid: string;
  state: SessionState;
  paired: boolean;
  sip_user?: string;
  sip_pass?: string;
  sip_url?: string;
};
