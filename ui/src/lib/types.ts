// The slice of the Go `page` value the shell reads. Every page is the same value
// the Go template gets (see internal/web/nextjson.go), so these are Go field
// names, capitalised, and anything a view needs beyond them is read as `any`.

export interface OrgNode {
  ID: string;
  Name: string;
  Open?: boolean;
  Selected?: boolean;
  Documents?: number;
  Children?: OrgNode[] | null;
}

export interface Page {
  Title: string;
  Path: string;
  Identity: null | { PrincipalID: string; DisplayName: string; ActingFor?: string; MustChangePassword?: boolean; Contractor?: boolean };
  Build: { Revision: string; Profile: string };
  Version: string;
  Message?: string;
  Unread?: number;
  Undecided?: number;
  ToApprove?: number;
  ApprovalsWaiting?: number;
  TimeOffPending?: number;
  Org?: OrgNode[] | null;
  CanManageSpaces?: boolean;
  Can?: Record<string, boolean> | null;
  SalesNav?: boolean;
  IsAdmin?: boolean;
  NeedsDepartment?: boolean;
  DeptApplyNow?: boolean;
  Crumbs?: { ID: string; Name: string }[] | null;
  Timer?: null | { DocumentID?: string; DocTitle?: string; Elapsed: number }; // Elapsed is minutes, written by the walk from the entry's method
  Wide?: boolean;
  SSO?: boolean;
  SSOName?: string;
  HideLocalLogin?: boolean;
  Next?: string;
  [k: string]: unknown;
}

export interface Envelope {
  template: string;
  page: Page;
  assets: { css: string[]; scripts: string[] };
}
