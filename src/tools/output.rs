use std::{
    path::{Path, PathBuf},
    sync::atomic::{AtomicU64, Ordering},
};

use tokio::{
    fs::{File, OpenOptions},
    io::AsyncWriteExt,
};

use super::tool_registry::ToolError;

pub(super) const MAX_OUTPUT_BYTES: usize = 50 * 1024;
pub(super) const MAX_OUTPUT_LINES: usize = 2000;

pub(super) struct OutputAccumulator {
    tail: Vec<u8>,
    total_bytes: usize,
    newlines: usize,
    ends_with_newline: bool,
    file: Option<File>,
    pub path: Option<PathBuf>,
}

impl OutputAccumulator {
    pub fn new() -> Self {
        Self {
            tail: Vec::new(),
            total_bytes: 0,
            newlines: 0,
            ends_with_newline: false,
            file: None,
            path: None,
        }
    }

    pub async fn append(&mut self, bytes: &[u8], directory: &Path) -> Result<(), ToolError> {
        self.total_bytes += bytes.len();
        self.newlines += bytes.iter().filter(|byte| **byte == b'\n').count();
        if let Some(last) = bytes.last() {
            self.ends_with_newline = *last == b'\n';
        }
        let lines = self.newlines + usize::from(self.total_bytes > 0 && !self.ends_with_newline);
        if let Some(file) = &mut self.file {
            file.write_all(bytes).await?;
        }
        self.tail.extend_from_slice(bytes);
        if self.file.is_none()
            && (self.total_bytes > MAX_OUTPUT_BYTES
                || lines > MAX_OUTPUT_LINES
                || String::from_utf8_lossy(&self.tail).len() > MAX_OUTPUT_BYTES)
        {
            static NEXT: AtomicU64 = AtomicU64::new(0);
            tokio::fs::create_dir_all(directory).await?;
            let path = directory.join(format!(
                "{}-{}.log",
                std::process::id(),
                NEXT.fetch_add(1, Ordering::Relaxed)
            ));
            let mut file = OpenOptions::new()
                .write(true)
                .create_new(true)
                .open(&path)
                .await?;
            file.write_all(&self.tail).await?;
            self.file = Some(file);
            self.path = Some(path);
        }
        if self.tail.len() > MAX_OUTPUT_BYTES {
            self.tail.drain(..self.tail.len() - MAX_OUTPUT_BYTES);
        }
        Ok(())
    }

    pub async fn finish(&mut self) -> Result<(), ToolError> {
        if let Some(file) = &mut self.file {
            file.flush().await?;
        }
        Ok(())
    }

    pub fn preview(&self) -> String {
        let mut start = 0;
        if self.path.is_some() {
            while start < self.tail.len() && self.tail[start] & 0xc0 == 0x80 {
                start += 1;
            }
        }
        let text = String::from_utf8_lossy(&self.tail[start..]);
        let mut start = text.len().saturating_sub(MAX_OUTPUT_BYTES);
        while !text.is_char_boundary(start) {
            start += 1;
        }
        let text = &text[start..];
        let lines: Vec<_> = text.split_inclusive('\n').collect();
        lines[lines.len().saturating_sub(MAX_OUTPUT_LINES)..].concat()
    }
}
