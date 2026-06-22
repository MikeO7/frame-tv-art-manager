package health

const uploadHTML = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Frame TV Art Uploader</title>
    <style>
        :root {
            --bg-color: #0b0f19;
            --card-bg: rgba(20, 26, 42, 0.6);
            --border-color: rgba(255, 255, 255, 0.08);
            --text-color: #f3f4f6;
            --text-muted: #9ca3af;
            --primary-color: #6366f1;
            --primary-hover: #4f46e5;
            --success-color: #10b981;
            --error-color: #ef4444;
        }
        body {
            font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
            background-color: var(--bg-color);
            color: var(--text-color);
            margin: 0;
            display: flex;
            align-items: center;
            justify-content: center;
            min-height: 100vh;
            padding: 20px;
            box-sizing: border-box;
        }
        .container {
            width: 100%;
            max-width: 500px;
            background: var(--card-bg);
            border: 1px solid var(--border-color);
            border-radius: 24px;
            padding: 32px;
            box-shadow: 0 20px 40px rgba(0, 0, 0, 0.3);
            backdrop-filter: blur(16px);
        }
        h1 {
            font-size: 24px;
            margin-top: 0;
            margin-bottom: 8px;
            font-weight: 700;
            letter-spacing: -0.5px;
            text-align: center;
        }
        .subtitle {
            color: var(--text-muted);
            text-align: center;
            font-size: 14px;
            margin-bottom: 32px;
        }
        .dropzone {
            border: 2px dashed rgba(99, 102, 241, 0.3);
            border-radius: 16px;
            padding: 40px 20px;
            text-align: center;
            cursor: pointer;
            transition: all 0.2s ease;
            background: rgba(99, 102, 241, 0.02);
        }
        .dropzone:hover, .dropzone.dragover {
            border-color: var(--primary-color);
            background: rgba(99, 102, 241, 0.06);
        }
        .dropzone svg {
            width: 48px;
            height: 48px;
            color: var(--primary-color);
            margin-bottom: 16px;
        }
        .dropzone p {
            margin: 0;
            font-size: 15px;
            font-weight: 500;
        }
        .dropzone span {
            display: block;
            font-size: 12px;
            color: var(--text-muted);
            margin-top: 8px;
        }
        #file-input {
            display: none;
        }
        .file-list {
            margin-top: 24px;
            max-height: 200px;
            overflow-y: auto;
        }
        .file-item {
            display: flex;
            align-items: center;
            justify-content: space-between;
            padding: 12px;
            background: rgba(255, 255, 255, 0.02);
            border: 1px solid var(--border-color);
            border-radius: 12px;
            margin-bottom: 8px;
            font-size: 13px;
        }
        .file-name {
            font-weight: 500;
            overflow: hidden;
            text-overflow: ellipsis;
            white-space: nowrap;
            max-width: 200px;
        }
        .file-status {
            font-weight: 600;
        }
        .status-uploading { color: var(--primary-color); }
        .status-success { color: var(--success-color); }
        .status-error { color: var(--error-color); }
        .progress-bar {
            height: 4px;
            width: 100%;
            background: rgba(255, 255, 255, 0.05);
            border-radius: 2px;
            margin-top: 8px;
            overflow: hidden;
            display: none;
        }
        .progress-inner {
            height: 100%;
            background: var(--primary-color);
            width: 0%;
            transition: width 0.1s ease;
        }
    </style>
</head>
<body>
    <div class="container">
        <h1>Frame TV Art Uploader</h1>
        <div class="subtitle">Upload JPEG or PNG images directly to your TV</div>

        <label for="file-input">
            <div class="dropzone" id="dropzone">
                <svg xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
                    <path stroke-linecap="round" stroke-linejoin="round" d="M12 16.5V9.75m0 0l3 3m-3-3l-3 3M6.75 19.5a4.5 4.5 0 01-1.41-8.775 5.25 5.25 0 0110.233-2.33 3 3 0 013.758 3.848A3.752 3.752 0 0118 19.5H6.75z" />
                </svg>
                <p>Tap to select or drag photos here</p>
                <span>Supports multiple JPEGs and PNGs</span>
            </div>
        </label>
        <input type="file" id="file-input" accept="image/jpeg,image/png" multiple>

        <div class="progress-bar" id="progress-bar">
            <div class="progress-inner" id="progress-inner"></div>
        </div>

        <div class="file-list" id="file-list"></div>
    </div>

    <script>
        const dropzone = document.getElementById('dropzone');
        const fileInput = document.getElementById('file-input');
        const fileList = document.getElementById('file-list');
        const progressBar = document.getElementById('progress-bar');
        const progressInner = document.getElementById('progress-inner');

        // Drag and drop events
        ['dragenter', 'dragover'].forEach(eventName => {
            dropzone.addEventListener(eventName, (e) => {
                e.preventDefault();
                dropzone.classList.add('dragover');
            }, false);
        });

        ['dragleave', 'drop'].forEach(eventName => {
            dropzone.addEventListener(eventName, (e) => {
                e.preventDefault();
                dropzone.classList.remove('dragover');
            }, false);
        });

        dropzone.addEventListener('drop', (e) => {
            const dt = e.dataTransfer;
            const files = dt.files;
            handleFiles(files);
        });

        fileInput.addEventListener('change', (e) => {
            handleFiles(e.target.files);
        });

        async function handleFiles(files) {
            if (files.length === 0) return;

            progressBar.style.display = 'block';
            progressInner.style.width = '0%';

            for (let i = 0; i < files.length; i++) {
                const file = files[i];
                const item = document.createElement('div');
                item.className = 'file-item';
                item.innerHTML = '<span class=\"file-name\">' + file.name + '</span><span class=\"file-status status-uploading\">Uploading...</span>';
                fileList.insertBefore(item, fileList.firstChild);

                const statusSpan = item.querySelector('.file-status');

                const formData = new FormData();
                formData.append('file', file);

                try {
                    const response = await fetch('/upload', {
                        method: 'POST',
                        body: formData
                    });

                    const result = await response.json();
                    if (response.ok && result.status === 'ok') {
                        statusSpan.className = 'file-status status-success';
                        statusSpan.textContent = result.message.includes('exists') ? 'Deduplicated' : 'Success';
                    } else {
                        statusSpan.className = 'file-status status-error';
                        statusSpan.textContent = result.error || 'Failed';
                    }
                } catch (err) {
                    statusSpan.className = 'file-status status-error';
                    statusSpan.textContent = 'Network Error';
                }

                progressInner.style.width = ((i + 1) / files.length) * 100 + '%';
            }

            setTimeout(() => {
                progressBar.style.display = 'none';
            }, 1000);
        }
    </script>
</body>
</html>`
