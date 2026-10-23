import { Marked } from 'marked';
import { markedHighlight } from 'marked-highlight';
import hljs from 'highlight.js';

// Match web/src/lib/Editor.svelte without importing the app's node_modules or
// bundling a renderer in the extension. All three dependencies are dev-only.
export const marked = new Marked(markedHighlight({
  langPrefix: 'hljs language-',
  highlight(code, lang) {
    const language = hljs.getLanguage(lang) ? lang : 'plaintext';
    return hljs.highlight(code, { language }).value;
  },
}));
