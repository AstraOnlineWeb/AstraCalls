export type SessionState =
  | "connecting"
  | "qr"
  | "open"
  | "logged_out"
  | "pairing_code"
  | "passkey_request";

export type SessionInfo = {
  id: string;
  name: string;
  jid: string;
  lastJid: string;
  state: SessionState;
  paired: boolean;
  recording: boolean;
  sip_user?: string;
  sip_pass?: string;
  sip_url?: string;
};
