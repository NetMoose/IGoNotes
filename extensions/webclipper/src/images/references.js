import { fromMarkdown } from 'mdast-util-from-markdown';
import { gfmFromMarkdown } from 'mdast-util-gfm';
import { gfm } from 'micromark-extension-gfm';

// Pure scanner, not a renderer or serializer. Offsets index the original JS
// string (UTF-16), including CRLF. Compiler enter hooks observe micromark's
// already-disambiguated tokens; default exit handlers still decode the values.
// No candidate reparsing, delimiter guessing, DOM, network or serialization.
export function scanImageReferences(markdown, sourceUrl) {
  const position = token => ({ start: { ...token.start }, end: { ...token.end } });
  const ranges = { enter: {
    labelText(token) {
      const node = this.stack[this.stack.length - 2];
      if (node.type === 'image') node.labelPosition = position(token);
    },
    resource(token) {
      const node = this.stack[this.stack.length - 1];
      if (node.type === 'image') {
        // Empty bare destination has no string token. Insert immediately after
        // the opening parenthesis, retaining its following whitespace/title.
        const point = { ...token.start, offset: token.start.offset + 1, column: token.start.column + 1 };
        node.destinationPosition = { start: point, end: { ...point } };
      }
    },
    resourceDestinationLiteral(token) {
      const node = this.stack[this.stack.length - 1];
      if (node.type === 'image') {
        const point = { ...token.start, offset: token.start.offset + 1, column: token.start.column + 1 };
        node.destinationPosition = { start: point, end: { ...point } };
      }
    },
    resourceDestinationString(token) {
      const node = this.stack[this.stack.length - 1];
      if (node.type === 'image') node.destinationPosition = position(token);
      this.buffer();
    },
  } };
  const tree = fromMarkdown(markdown, { extensions: [gfm()], mdastExtensions: [gfmFromMarkdown(), ranges] });
  const definitions = new Map(), nodes = [];
  const visit = node => {
    nodes.push(node);
    for (const child of node.children ?? []) visit(child);
  };
  visit(tree);
  // mdast identifiers already implement CommonMark whitespace/case folding.
  for (const node of nodes) if (node.type === 'definition' && !definitions.has(node.identifier)) definitions.set(node.identifier, node);
  const refs = [];
  for (const node of nodes) {
    if (!['image', 'imageReference'].includes(node.type)) continue;
    const definition = node.type === 'imageReference' ? definitions.get(node.identifier) : undefined;
    if (node.type === 'imageReference' && !definition) continue;
    const destination = definition?.url ?? node.url;
    // Keep the decoded destination as well as the resolved alias. URL policy is
    // acquisition's responsibility; unsupported edited sources become errors.
    let url = destination;
    if (!/^data:/i.test(destination)) {
      try { url = new URL(destination, sourceUrl).href; } catch { /* explicit unsafe result later */ }
    }
    refs.push({ url, destination, kind: node.referenceType ?? 'inline', position: node.position, labelPosition: node.labelPosition, destinationPosition: node.destinationPosition, alt: node.alt, title: definition?.title ?? node.title,
      ...(definition ? { identifier: node.identifier, definition: { url: definition.url, title: definition.title, position: definition.position } } : {}) });
  }
  return refs;
}
