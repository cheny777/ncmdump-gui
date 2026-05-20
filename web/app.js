function updateLog(text) {
    document.getElementById('log').textContent = text;
}

async function submitSingle() {
    const form = document.getElementById('singleForm');
    const data = new FormData(form);
    updateLog('正在转换单个文件，请稍候...');
    const resp = await fetch('/convert-file', { method: 'POST', body: data });
    const text = await resp.text();
    updateLog(text);
}

async function submitBatch() {
    const form = document.getElementById('batchForm');
    const data = new FormData(form);
    updateLog('正在转换文件夹中的全部 .ncm 文件，请稍候...');
    const resp = await fetch('/convert-folder', { method: 'POST', body: data });
    const text = await resp.text();
    updateLog(text);
}

async function chooseTargetFolder() {
    updateLog('正在打开目标文件夹选择器...');
    const resp = await fetch('/choose-target');
    if (resp.status === 204) {
        updateLog('已取消选择目标文件夹。');
        return;
    }
    if (!resp.ok) {
        const msg = await resp.text();
        updateLog('选择目标文件夹失败：' + msg);
        return;
    }
    const folder = await resp.text();
    document.getElementById('singleTarget').value = folder;
    document.getElementById('batchTarget').value = folder;
    updateLog('已选择目标文件夹：' + folder);
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
