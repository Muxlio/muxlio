// Canvas AST and local interaction engine contracts.

type CanvasStateValue = string | number | boolean;

interface CanvasDataFilter {
  field: string;
  equalsState?: string;
  equals?: unknown;
}

interface CanvasDataRef {
  source: string;
  pointer?: string;
  filter?: CanvasDataFilter;
}

interface CanvasSourceValue {
  value: unknown;
}

interface CanvasSourcePort {
  artifactPort: string;
}

type CanvasSource = CanvasSourceValue | CanvasSourcePort;

interface CanvasSelectOption {
  value: string | number;
  label: string;
}

interface CanvasControlSelect {
  type: "select";
  id: string;
  label: string;
  initial?: string | number;
  options: readonly CanvasSelectOption[];
}

interface CanvasControlToggle {
  type: "toggle";
  id: string;
  label: string;
  initial?: boolean;
}

interface CanvasControlSearch {
  type: "search";
  id: string;
  label?: string;
  placeholder?: string;
  initial?: string;
}

type CanvasControl =
  | CanvasControlSelect
  | CanvasControlToggle
  | CanvasControlSearch;

interface CanvasTableColumn {
  field: string;
  label: string;
  kind?: "text" | "integer" | "number" | "percent" | "date" | "badge";
}

interface CanvasSummaryItem {
  label: string;
  value: string | number;
  tone?: "green" | "red" | "amber" | "neutral";
}

interface CanvasTabItem {
  id: string;
  label: string;
  children: readonly CanvasNode[];
}

interface CanvasNodeBase {
  type: string;
  id: string;
}

interface CanvasStackNode extends CanvasNodeBase {
  type: "stack";
  gap?: "none" | "compact" | "comfortable" | "spacious";
  align?: "start" | "center" | "end" | "stretch";
  children: readonly CanvasNode[];
}

interface CanvasGridNode extends CanvasNodeBase {
  type: "grid";
  columns?: number | "auto-fit";
  gap?: "none" | "compact" | "comfortable" | "spacious";
  children: readonly CanvasNode[];
}

interface CanvasRowNode extends CanvasNodeBase {
  type: "row";
  gap?: "none" | "compact" | "comfortable" | "spacious";
  align?: "start" | "center" | "end" | "stretch";
  justify?: "start" | "center" | "end" | "between" | "around";
  wrap?: boolean;
  children: readonly CanvasNode[];
}

interface CanvasSectionNode extends CanvasNodeBase {
  type: "section";
  title?: string;
  description?: string;
  collapsible?: boolean;
  collapsed?: boolean;
  children: readonly CanvasNode[];
}

interface CanvasTabsNode extends CanvasNodeBase {
  type: "tabs";
  initial?: string;
  tabs: readonly CanvasTabItem[];
}

interface CanvasDividerNode extends CanvasNodeBase {
  type: "divider";
  spacing?: "none" | "compact" | "comfortable" | "spacious";
}

interface CanvasHeadingNode extends CanvasNodeBase {
  type: "heading";
  level: number;
  text: string;
}

interface CanvasTextNode extends CanvasNodeBase {
  type: "text";
  text: string;
  variant?: "body" | "caption" | "lead" | "code" | "muted";
}

interface CanvasMarkdownNode extends CanvasNodeBase {
  type: "markdown";
  text: string;
}

interface CanvasCodeNode extends CanvasNodeBase {
  type: "code";
  text: string;
  language?: string;
}

interface CanvasCalloutNode extends CanvasNodeBase {
  type: "callout";
  tone?: "info" | "success" | "warning" | "error";
  title?: string;
  text: string;
}

interface CanvasMetricNode extends CanvasNodeBase {
  type: "metric";
  label: string;
  value: string | number;
  note?: string;
  tone?: "green" | "red" | "amber" | "neutral";
}

interface CanvasKeyValueNode extends CanvasNodeBase {
  type: "keyValue";
  label: string;
  value: string | number | boolean;
  note?: string;
}

interface CanvasStatusNode extends CanvasNodeBase {
  type: "status";
  label: string;
  state: string;
  tone?: "green" | "red" | "amber" | "neutral" | "blue";
}

interface CanvasSummaryNode extends CanvasNodeBase {
  type: "summary";
  items: readonly CanvasSummaryItem[];
}

interface CanvasTableNode extends CanvasNodeBase {
  type: "table";
  title?: string;
  data: CanvasDataRef | readonly Record<string, unknown>[];
  columns: readonly CanvasTableColumn[];
  sortable?: boolean;
  pageSize?: number;
}

interface CanvasJsonNode extends CanvasNodeBase {
  type: "json";
  title?: string;
  data: CanvasDataRef | unknown;
  collapsed?: boolean;
}

interface CanvasBarChartNode extends CanvasNodeBase {
  type: "barChart";
  title?: string;
  data: CanvasDataRef | readonly Record<string, unknown>[];
  categoryField: string;
  valueField: string;
  format?: "number" | "percent" | "bytes" | "duration";
  selectedKey?: string;
}

interface CanvasSparklineNode extends CanvasNodeBase {
  type: "sparkline";
  title?: string;
  data: CanvasDataRef | readonly unknown[];
  valueField?: string;
  labelField?: string;
}

interface CanvasArtifactLinkNode extends CanvasNodeBase {
  type: "artifactLink";
  port: string;
  label: string;
  description?: string;
}

type CanvasNode =
  | CanvasStackNode
  | CanvasGridNode
  | CanvasRowNode
  | CanvasSectionNode
  | CanvasTabsNode
  | CanvasDividerNode
  | CanvasHeadingNode
  | CanvasTextNode
  | CanvasMarkdownNode
  | CanvasCodeNode
  | CanvasCalloutNode
  | CanvasMetricNode
  | CanvasKeyValueNode
  | CanvasStatusNode
  | CanvasSummaryNode
  | CanvasTableNode
  | CanvasJsonNode
  | CanvasBarChartNode
  | CanvasSparklineNode
  | CanvasControlSelect
  | CanvasControlToggle
  | CanvasControlSearch
  | CanvasArtifactLinkNode;

interface CanvasDocument {
  version: 1;
  title: string;
  description?: string;
  sources?: Record<string, CanvasSource>;
  controls?: Record<string, CanvasControl>;
  presentation?: {
    density?: "compact" | "comfortable" | "spacious";
  };
  content: CanvasNode;
}

interface CanvasSiblingOutput {
  digest: string;
  mime: string;
  path: string;
  size?: number;
}

interface CanvasRendererContext {
  sources: ReadonlyMap<string, unknown>;
  state: ReadonlyMap<string, CanvasStateValue>;
  controlTypes?: ReadonlyMap<string, CanvasControl["type"]>;
  resolveData(ref: unknown): unknown;
  siblingOutputs?: Readonly<Record<string, CanvasSiblingOutput>>;
  runId?: string;
  activationId?: string;
  tableSortState?: ReadonlyMap<string, { field: string; direction: "asc" | "desc" }>;
  tablePageState?: ReadonlyMap<string, number>;
  activeTabsState?: ReadonlyMap<string, string>;
  selectedPointState?: ReadonlyMap<string, string>;
  collapsedSections?: ReadonlySet<string>;
}

type CanvasRenderer = (
  node: CanvasNode,
  context: CanvasRendererContext,
) => ComponentHTML;

type CanvasEvent =
  | { type: "setState"; stateId: string; value: CanvasStateValue }
  | { type: "selectTab"; componentId: string; tabId: string }
  | { type: "sortTable"; componentId: string; field: string }
  | { type: "setPage"; componentId: string; page: number }
  | { type: "selectChartPoint"; componentId: string; key: string }
  | { type: "toggleSection"; componentId: string };
