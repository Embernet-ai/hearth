/** The screens that read the width of the window and fill it, instead of sitting in a centered column. The rest of the app keeps its column. */
export const FILL = new Set(['dashboard', 'inbox', 'outbox', 'settings', 'unassigned', 'channels', 'contacts']);
export const fills = (template: string) => FILL.has(template);
