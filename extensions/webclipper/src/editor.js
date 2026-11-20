import { mount } from 'svelte';
import Editor from './ui/Editor.svelte';
import './ui/style.css';

mount(Editor, { target: document.getElementById('app') });
