-- The dedicated editor runtime: no user configuration, plugins, or image UI.
vim.opt.runtimepath = { vim.env.VIMRUNTIME }
vim.opt.packpath = { vim.env.VIMRUNTIME }
vim.opt.termguicolors = true
vim.opt.number = true
vim.opt.hidden = true
vim.opt.modeline = false
vim.opt.swapfile = false
vim.opt.undofile = false
vim.opt.autoindent = true
vim.opt.fixendofline = false
vim.opt.expandtab = true
vim.opt.shiftwidth = 2
vim.opt.tabstop = 2
vim.opt.mouse = 'a'
vim.opt.showmode = true
vim.opt.signcolumn = 'yes'
vim.opt.updatetime = 150
vim.opt.shortmess:append('I')
-- Newer built-in Markdown ftplugins require a parser. Keep filetype detection
-- independent so missing optional Tree-sitter parsers cannot prevent opening.
vim.cmd('filetype plugin indent off')
vim.cmd('filetype on')
vim.cmd('syntax enable')
vim.filetype.add({ extension = { mmd = 'mermaid', mermaid = 'mermaid', mdx = 'markdown' } })

local M = { channel = nil, attached = {}, pending = {}, virtual = {} }
_G.lazymermaid = M
local ns = vim.api.nvim_create_namespace('lazymermaid.diagnostics')
vim.diagnostic.config({ underline = true, signs = true, virtual_text = true }, ns)

local function source(buf)
  local s = table.concat(vim.api.nvim_buf_get_lines(buf, 0, -1, true), '\n')
  if vim.bo[buf].endofline then s = s .. '\n' end
  return s
end

local function set_source(buf, text)
  local eol = text:sub(-1) == '\n'
  if eol then text = text:sub(1, -2) end
  local lines = vim.split(text, '\n', { plain = true })
  vim.api.nvim_buf_set_lines(buf, 0, -1, true, lines)
  vim.bo[buf].endofline = eol
  vim.bo[buf].fixendofline = false
end

function M.snapshot(buf)
  buf = buf or vim.api.nvim_get_current_buf()
  local virtual = M.virtual[buf]
  return {
    buffer = buf,
    path = vim.api.nvim_buf_get_name(buf),
    source = source(buf),
    changed_tick = vim.api.nvim_buf_get_changedtick(buf),
    cursor_line = buf == vim.api.nvim_get_current_buf() and vim.api.nvim_win_get_cursor(0)[1] or 1,
    dirty = vim.bo[buf].modified,
    virtual = virtual ~= nil,
    parent_path = virtual and virtual.path or '',
    body_start_line = virtual and virtual.body_start_line or 1,
  }
end

function M.notify(buf)
  if not M.channel or not vim.api.nvim_buf_is_loaded(buf) then return end
  -- Background buffers are included in dirty-buffer queries, but previews follow
  -- the selected editor document only.
  if buf ~= vim.api.nvim_get_current_buf() then return end
  vim.rpcnotify(M.channel, 'lazymermaid_changed', vim.json.encode(M.snapshot(buf)))
end

local function schedule_notify(buf)
  if M.pending[buf] then return end
  M.pending[buf] = true
  vim.schedule(function()
    M.pending[buf] = nil
    M.notify(buf)
  end)
end

function M.attach(buf)
  if M.attached[buf] or not vim.api.nvim_buf_is_loaded(buf) then return end
  M.attached[buf] = vim.api.nvim_buf_attach(buf, false, {
    on_lines = function(_, b) schedule_notify(b) end,
    on_reload = function(_, b) schedule_notify(b) end,
    on_detach = function(_, b) M.attached[b] = nil end,
  })
end

function M.connect(channel, runtime_paths)
  M.channel = channel
  for _, path in ipairs(type(runtime_paths) == 'table' and runtime_paths or {}) do vim.opt.runtimepath:append(path) end
  M.attach(vim.api.nvim_get_current_buf())
  M.notify(vim.api.nvim_get_current_buf())
end

vim.api.nvim_create_autocmd({ 'BufEnter', 'BufReadPost', 'BufNewFile', 'BufWritePost' }, {
  callback = function(ev) M.attach(ev.buf); schedule_notify(ev.buf) end,
})
vim.api.nvim_create_autocmd('FileType', {
  callback = function(ev)
    -- Highlighting is optional and never validation. Only bundled/system runtime
    -- paths are available; no parser is downloaded during startup.
    pcall(vim.treesitter.start, ev.buf)
    vim.bo[ev.buf].autoindent = true
    vim.bo[ev.buf].indentexpr = ''
  end,
})

function M.open(path, line, col)
  local buf = vim.fn.bufadd(path)
  vim.fn.bufload(buf)
  vim.api.nvim_set_current_buf(buf)
  vim.cmd('checktime')
  local count = vim.api.nvim_buf_line_count(buf)
  line = math.max(1, math.min(line, count))
  local text = vim.api.nvim_buf_get_lines(buf, line - 1, line, true)[1] or ''
  vim.api.nvim_win_set_cursor(0, { line, math.max(0, math.min(col - 1, #text)) })
  M.attach(buf)
  M.notify(buf)
end

function M.scratch(text)
  local buf = vim.api.nvim_create_buf(true, false)
  vim.api.nvim_set_current_buf(buf)
  set_source(buf, text)
  vim.bo[buf].filetype = 'mermaid'
  -- Ordinary unnamed buffer: :write filename.mmd works natively.
  vim.bo[buf].modified = text ~= ''
  M.attach(buf)
  M.notify(buf)
end

function M.parent(path)
  local buf = vim.fn.bufadd(path)
  vim.fn.bufload(buf)
  vim.api.nvim_buf_call(buf, function() vim.cmd('checktime') end)
  if vim.bo[buf].modified then error('Save the source document before opening a virtual block') end
  return vim.json.encode(M.snapshot(buf))
end

local function serialization(buf)
  return {
    fileformat = vim.bo[buf].fileformat,
    fileencoding = vim.bo[buf].fileencoding,
    endofline = vim.bo[buf].endofline,
    fixendofline = vim.bo[buf].fixendofline,
    bomb = vim.bo[buf].bomb,
  }
end

function M.open_virtual(token, path, parent, tick, text, body_start_line)
  if vim.bo[parent].modified or vim.api.nvim_buf_get_changedtick(parent) ~= tick then
    error('Source changed while opening the virtual block')
  end
  local buf = vim.api.nvim_create_buf(true, false)
  vim.api.nvim_buf_set_name(buf, 'lazymermaid://' .. token .. '/diagram.mmd')
  vim.bo[buf].buftype = 'acwrite'
  vim.bo[buf].bufhidden = 'hide'
  set_source(buf, text)
  vim.bo[buf].filetype = 'mermaid'
  vim.bo[buf].modified = false
  M.virtual[buf] = {
    token = token, path = path, parent = parent, tick = tick, body_start_line = body_start_line,
    parent_name = vim.api.nvim_buf_get_name(parent), serialization = serialization(parent), detached = false,
  }
  vim.api.nvim_create_autocmd('BufWriteCmd', {
    buffer = buf,
    callback = function()
      local v = M.virtual[buf]
      if v.detached then
        error('Virtual projection is detached; save the raw source and reopen the block. Virtual draft retained.')
      end
      if not vim.api.nvim_buf_is_loaded(v.parent) then error('Source buffer was unloaded; draft retained') end
      if vim.api.nvim_buf_get_name(v.parent) ~= v.parent_name then
        v.detached = true
        error('Source buffer was renamed; virtual projection detached. Reopen the block; draft retained.')
      end
      if not vim.deep_equal(serialization(v.parent), v.serialization) then
        v.detached = true
        error('Source serialization options changed; virtual projection detached. Save the raw source and reopen the block; draft retained.')
      end
      if vim.bo[v.parent].modified or vim.api.nvim_buf_get_changedtick(v.parent) ~= v.tick then
        error('Source changed; virtual draft retained. Reopen the block after resolving the source.')
      end
      local before = source(v.parent)
      local draft_tick = vim.api.nvim_buf_get_changedtick(buf)
      local result = vim.json.decode(vim.rpcrequest(M.channel, 'lazymermaid_save_virtual', vim.json.encode({
        token = v.token, parent = before, parent_tick = v.tick, body = source(buf),
      })))
      if result.error then error(result.error .. '; draft retained') end
      -- This callback is synchronous so :wq waits for success. The Go callback
      -- only computes a patch and checks disk; it never calls back into Neovim.
      local ok, err = pcall(function()
        set_source(v.parent, result.source)
        vim.api.nvim_buf_call(v.parent, function() vim.cmd('silent keepalt write') end)
      end)
      if not ok then
        -- A native write can fail after touching the file. Never roll back the
        -- applied source or claim it is saved. Keep both recoverable buffers,
        -- and never reuse this projection's old byte ranges after failure.
        vim.bo[v.parent].modified = true
        vim.bo[buf].modified = true
        v.detached = true
        error('Could not save source: ' .. tostring(err)
          .. '; applied source and virtual draft retained. Projection detached; save the raw source and reopen the block.')
      end
      v.tick = vim.api.nvim_buf_get_changedtick(v.parent)
      v.serialization = serialization(v.parent)
      -- Refresh the baseline synchronously before another :write can arrive.
      vim.rpcrequest(M.channel, 'lazymermaid_virtual_saved', vim.json.encode({
        token = v.token, parent = source(v.parent), parent_tick = v.tick,
      }))
      if vim.api.nvim_buf_get_changedtick(buf) == draft_tick then vim.bo[buf].modified = false end
      M.notify(buf)
    end,
  })
  vim.api.nvim_set_current_buf(buf)
  M.attach(buf)
  M.notify(buf)
end

function M.diagnostics(buf, tick, diagnostics)
  if not vim.api.nvim_buf_is_loaded(buf) or vim.api.nvim_buf_get_changedtick(buf) ~= tick then return end
  vim.diagnostic.set(ns, buf, diagnostics)
end

function M.dirty()
  local result = {}
  for _, buf in ipairs(vim.api.nvim_list_bufs()) do
    if vim.api.nvim_buf_is_loaded(buf) and vim.bo[buf].modified then
      table.insert(result, { buffer = buf, path = vim.api.nvim_buf_get_name(buf), virtual = M.virtual[buf] ~= nil })
    end
  end
  return vim.json.encode(result)
end
