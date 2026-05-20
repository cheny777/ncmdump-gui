function setLog(text) {
    const log = document.getElementById('log');
    log.textContent = text;
    log.scrollTop = log.scrollHeight;
}

function appendLog(text) {
    const log = document.getElementById('log');
    log.textContent += text;
    log.scrollTop = log.scrollHeight;
}

function setProgress(percent) {
    const bar = document.getElementById('batchProgress');
    const text = document.getElementById('batchProgressText');
    const normalized = Math.min(100, Math.max(0, percent));
    bar.value = normalized;
    text.textContent = normalized + '%';
}

function updateProgressFromText(text) {
    const regex = /已完成\s*(\d+)\/(\d+)/g;
    let match;
    while ((match = regex.exec(text)) !== null) {
        const done = Number(match[1]);
        const total = Number(match[2]);
        if (total > 0) {
            setProgress(Math.round((done / total) * 100));
        }
    }
}

async function submitSingle() {
    const form = document.getElementById('singleForm');
    const data = new FormData(form);
    setLog('正在转换单个文件，请稍候...\n');
    const resp = await fetch('/convert-file', { method: 'POST', body: data });
    const text = await resp.text();
    setLog(text + '\n');
}

async function submitBatch() {
    const form = document.getElementById('batchForm');
    const data = new FormData(form);
    setLog('正在转换文件夹中的全部 .ncm 文件，请稍候...\n');
    setProgress(0);

    const resp = await fetch('/convert-folder', { method: 'POST', body: data });
    if (!resp.ok) {
        const text = await resp.text();
        setLog(text + '\n');
        setProgress(0);
        return;
    }
    if (!resp.body) {
        const text = await resp.text();
        setLog(text + '\n批量转换已完成。\n');
        setProgress(100);
        return;
    }

    const reader = resp.body.getReader();
    const decoder = new TextDecoder();
    let buffer = '';
    while (true) {
        const { done, value } = await reader.read();
        if (done) {
            break;
        }
        buffer += decoder.decode(value, { stream: true });
        const lastNewline = buffer.lastIndexOf('\n');
        if (lastNewline !== -1) {
            const chunk = buffer.slice(0, lastNewline + 1);
            buffer = buffer.slice(lastNewline + 1);
            appendLog(chunk);
            updateProgressFromText(chunk);
        }
    }
    if (buffer) {
        appendLog(buffer);
        updateProgressFromText(buffer);
    }

    setProgress(100);
    appendLog('\n批量转换已完成。\n');
}

async function chooseTargetFolder() {
    setLog('正在打开目标文件夹选择器...\n');
    const resp = await fetch('/choose-target');
    if (resp.status === 204) {
        appendLog('已取消选择目标文件夹。\n');
        return;
    }
    if (!resp.ok) {
        const msg = await resp.text();
        appendLog('选择目标文件夹失败：' + msg + '\n');
        return;
    }
    const folder = await resp.text();
    document.getElementById('singleTarget').value = folder;
    document.getElementById('batchTarget').value = folder;
    appendLog('已选择目标文件夹：' + folder + '\n');
}

function triggerSingleFile() {
    document.getElementById('singleInput').click();
}

function triggerFolderInput() {
    document.getElementById('folderInput').click();
}

document.addEventListener('DOMContentLoaded', () => {
    const singleInput = document.getElementById('singleInput');
    const singleFileName = document.getElementById('singleFileName');
    const batchInput = document.getElementById('folderInput');
    const batchFileName = document.getElementById('batchFileName');

    singleInput.addEventListener('change', () => {
        if (singleInput.files.length > 0) {
            singleFileName.textContent = singleInput.files[0].name;
        } else {
            singleFileName.textContent = '尚未选择文件';
        }
    });

    batchInput.addEventListener('change', () => {
        if (batchInput.files.length > 0) {
            batchFileName.textContent = batchInput.files.length + ' 个文件已选';
        } else {
            batchFileName.textContent = '尚未选择文件夹';
        }
    });
});
