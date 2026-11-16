// Recursive Canvas tree walker and depth/count guard.

function renderCanvasDocumentBody(
  doc: CanvasDocument,
  context: CanvasRendererContext,
): ComponentHTML {
  const controls = Object.entries(doc.controls || {})
    .map(
      ([key, ctrl]) =>
        renderCanvasNode(
          { ...ctrl, id: ctrl.id || key } as unknown as CanvasNode,
          context,
        ).value,
    )
    .join("");
  const content = renderCanvasNode(doc.content, context).value;
  return componentBody(
    (controls
      ? renderComponent("canvas-controls", {
          controls: componentBody(controls),
        })
      : "") + content,
  );
}

function renderCanvasNode(
  node: CanvasNode,
  context: CanvasRendererContext,
  depth = 1,
): ComponentHTML {
  if (!node || typeof node !== "object") {
    return componentBody("");
  }
  if (depth > 16) {
    return componentBody(
      renderComponent("canvas-text", {
        id: "depth-exceeded-" + (node.id || "node"),
        variantClass: "canvas-text--muted",
        text: "Component depth limit exceeded.",
      }),
    );
  }

  const renderChild = (child: CanvasNode) =>
    renderCanvasNode(child, context, depth + 1);

  switch (node.type) {
    case "stack":
      return renderCanvasStack(node as CanvasStackNode, context, renderChild);
    case "grid":
      return renderCanvasGrid(node as CanvasGridNode, context, renderChild);
    case "row":
      return renderCanvasRow(node as CanvasRowNode, context, renderChild);
    case "section":
      return renderCanvasSection(node as CanvasSectionNode, context, renderChild);
    case "tabs":
      return renderCanvasTabs(node as CanvasTabsNode, context, renderChild);
    case "divider":
      return renderCanvasDivider(node as CanvasDividerNode);
    case "heading":
      return renderCanvasHeading(node as CanvasHeadingNode);
    case "text":
      return renderCanvasText(node as CanvasTextNode);
    case "markdown":
      return renderCanvasMarkdown(node as CanvasMarkdownNode);
    case "code":
      return renderCanvasCode(node as CanvasCodeNode);
    case "callout":
      return renderCanvasCallout(node as CanvasCalloutNode);
    case "metric":
      return renderCanvasMetric(node as CanvasMetricNode);
    case "keyValue":
      return renderCanvasKeyValue(node as CanvasKeyValueNode);
    case "status":
      return renderCanvasStatus(node as CanvasStatusNode);
    case "summary":
      return renderCanvasSummary(node as CanvasSummaryNode);
    case "table":
      return renderCanvasTable(node as CanvasTableNode, context);
    case "json":
      return renderCanvasJson(node as CanvasJsonNode, context);
    case "barChart":
      return renderCanvasBarChart(node as CanvasBarChartNode, context);
    case "sparkline":
      return renderCanvasSparkline(node as CanvasSparklineNode, context);
    case "select":
      return renderCanvasSelect(node as CanvasControlSelect, context);
    case "toggle":
      return renderCanvasToggle(node as CanvasControlToggle, context);
    case "search":
      return renderCanvasSearch(node as CanvasControlSearch, context);
    case "artifactLink":
      return renderCanvasArtifactLink(node as CanvasArtifactLinkNode, context);
    default: {
      const anyNode = node as any;
      return componentBody(
        renderComponent("canvas-text", {
          id: "unknown-" + (anyNode.id || "comp"),
          variantClass: "canvas-text--muted",
          text: "Unsupported component type: " + (anyNode.type || "unknown"),
        }),
      );
    }
  }
}
