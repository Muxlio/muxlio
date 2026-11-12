// Controls are addressed by stable IDs and data attributes in the static shell.
// Individual controls share this structural type in the legacy view renderers.
type UIElement = HTMLElement & Pick<HTMLInputElement, 'value' | 'disabled' | 'checked' | 'select' | 'files'> & Pick<HTMLAnchorElement, 'href' | 'target' | 'download'> & Pick<HTMLDetailsElement, 'open'> & {
  _activitySignature?: string;
  _analyticsModel?: any;
  _snapshotMarkup?: string;
  _jsonRaw?: string;
  _copyLogsText?: string;
  _requestSignature?: string;
  _requestDefinition?: any;
  showModal(): void;
  reset(): void;
  elements: HTMLFormControlsCollection & Record<string, HTMLInputElement>;
};

declare var renderMarkdown: (text: unknown) => string;
declare var MuxlioJson: { [key: string]: (...args: any[]) => any };
interface Window { MuxlioJson: typeof MuxlioJson; }
