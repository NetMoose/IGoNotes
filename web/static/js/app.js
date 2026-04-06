document.addEventListener('DOMContentLoaded', () => {
    const editor = document.getElementById('editor');
    const preview = document.getElementById('preview');
    const previewBtn = document.getElementById('previewBtn');
    const notesTree = document.getElementById('notesTree');

    // Переключение режимов
    previewBtn.addEventListener('click', () => {
        if (preview.style.display === 'none') {
            preview.innerHTML = marked.parse(editor.value);
            preview.style.display = 'block';
            editor.style.display = 'none';
            previewBtn.textContent = 'Редактировать';
        } else {
            preview.style.display = 'none';
            editor.style.display = 'block';
            previewBtn.textContent = 'Просмотр';
        }
    });

    // Загрузка дерева заметок
    fetch('/api/notes')
        .then(response => response.json())
        .then(data => {
            renderTree(notesTree, data);
        });

    function renderTree(container, items) {
        items.forEach(item => {
            const li = document.createElement('li');
            if (item.type === 'dir') {
                li.textContent = item.name;
                const ul = document.createElement('ul');
                renderTree(ul, item.children);
                li.appendChild(ul);
            } else {
                li.textContent = item.name;
                li.addEventListener('click', () => loadNote(item.path));
            }
            container.appendChild(li);
        });
    }

    function loadNote(path) {
        fetch(`/api/note?path=${encodeURIComponent(path)}`)
            .then(response => response.text())
            .then(content => {
                editor.value = content;
                preview.innerHTML = marked.parse(content);
            });
    }

    // Автосохранение
    editor.addEventListener('input', debounce(() => {
        saveNote();
    }, 2000));

    function saveNote() {
        const path = prompt('Введите путь для сохранения:', 'notes/example.md');
        if (!path) return;

        fetch('/api/save', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ path, content: editor.value })
        });
    }

    function debounce(func, delay) {
        let timer;
        return function () {
            clearTimeout(timer);
            timer = setTimeout(func, delay);
        };
    }
});