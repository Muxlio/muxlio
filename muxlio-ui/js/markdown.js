/* Safe, dependency-free Markdown for agent-authored text. HTML is always text;
 * images and other embedded content are never loaded. Raw data stays untouched.
 */
(function(){
  'use strict';
  const escape=text=>String(text).replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));

  function safeHref(value){
    // Reject controls and backslashes before checking schemes. Escape only when
    // emitting HTML, so query strings and entity-looking text retain their value.
    if(/[\u0000-\u0020\u007f-\u009f\\]/.test(value))return false;
    if(/^(https?:\/\/|mailto:)/i.test(value))return true;
    return !/^[^/?#]*:/.test(value)&&/^(?:\/(?!\/)|\.{1,2}\/|[?#]|[\w.-]+(?:[/?#]|$))/.test(value);
  }

  function inline(text,depth=0,links=true){
    if(depth>20)return escape(text);
    // Index matching parentheses once. Unclosed streamed links must not scan
    // the entire remaining transcript again for every subsequent opener.
    const closes=new Map(),opens=[];
    if(links)for(let p=0;p<text.length;p++){
      if(text[p]==='(')opens.push(p);
      else if(text[p]===')'&&opens.length)closes.set(opens.pop(),p);
    }
    let result='',i=0;
    while(i<text.length){
      const rest=text.slice(i);
      let match;
      if(rest[0]==='\\'&&/^\\[!"#$%&'()*+,\-./:;<=>?@[\]\\^_`{|}~]/.test(rest)){
        result+=escape(rest[1]);i+=2;continue;
      }
      if((match=/^(`+)([\s\S]*?)\1(?!`)/.exec(rest))){
        result+=`<code>${escape(match[2])}</code>`;i+=match[0].length;continue;
      }
      // Balanced parentheses allow ordinary URLs such as /docs/function(x).
      if(links&&(match=/^\[([^\[\]\n]+)\]\(/.exec(rest))){
        const close=closes.get(i+match[0].length-1);
        if(close!==undefined){
          const end=close-i+1;
          const href=rest.slice(match[0].length,end-1);
          const label=inline(match[1],depth+1,false);
          result+=safeHref(href)?`<a href="${escape(href)}" rel="noopener noreferrer">${label}</a>`:label;
          i+=end;continue;
        }
      }
      if((match=/^(\*\*|__)(?=\S)(.+?)\1/.exec(rest))){
        result+=`<strong>${inline(match[2],depth+1,links)}</strong>`;i+=match[0].length;continue;
      }
      if((match=/^(\*|_)(?=\S)([^\n]+?)\1/.exec(rest))&&
          (match[1]!=='_'||!/[\w]/.test(text[i-1]||''))){
        result+=`<em>${inline(match[2],depth+1,links)}</em>`;i+=match[0].length;continue;
      }
      result+=escape(text[i++]);
    }
    return result;
  }

  const fence=line=>/^ {0,3}(`{3,}|~{3,})(.*)$/.exec(line);
  const heading=line=>/^ {0,3}(#{1,6})\s+(.+?)\s*$/.exec(line);
  const list=line=>/^( *)([-+*]|\d+[.)])\s+(.*)$/.exec(line);
  const rule=line=>/^ {0,3}(\*{3,}|-{3,}|_{3,})\s*$/.test(line);
  const special=line=>fence(line)||heading(line)||list(line)||rule(line)||/^ {0,3}>/.test(line);

  function blocks(text,depth=0){
    if(depth>20)return `<p>${escape(text)}</p>`;
    const lines=text.replace(/\r\n?/g,'\n').split('\n'),out=[];
    let i=0;
    while(i<lines.length){
      const line=lines[i];
      let match;
      if(!line.trim()){i++;continue}
      if((match=fence(line))){
        const marker=match[1],code=[];
        const closing=new RegExp(`^ {0,3}${marker[0]}{${marker.length},}\\s*$`);
        i++;
        while(i<lines.length&&!closing.test(lines[i]))code.push(lines[i++]);
        if(i<lines.length)i++;
        out.push(`<pre><code>${escape(code.join('\n'))}</code></pre>`);continue;
      }
      if((match=heading(line))){
        const level=match[1].length;
        out.push(`<h${level}>${inline(match[2].replace(/\s+#+\s*$/,''))}</h${level}>`);i++;continue;
      }
      if(rule(line)){out.push('<hr>');i++;continue}
      if(/^ {0,3}>/.test(line)){
        const quote=[];
        while(i<lines.length&&/^ {0,3}>/.test(lines[i]))quote.push(lines[i++].replace(/^ {0,3}> ?/,''));
        out.push(`<blockquote>${blocks(quote.join('\n'),depth+1)}</blockquote>`);continue;
      }
      if((match=list(line))){
        const indent=match[1].length,ordered=/\d/.test(match[2]),tag=ordered?'ol':'ul',items=[];
        const start=ordered?parseInt(match[2],10):1;
        while(i<lines.length){
          const item=list(lines[i]);
          if(!item||item[1].length!==indent||/\d/.test(item[2])!==ordered)break;
          const content=[item[3]],padding=item[0].length-item[3].length;
          i++;
          while(i<lines.length&&lines[i].trim()&&lines[i].startsWith(' '.repeat(padding))){
            content.push(lines[i++].slice(padding));
          }
          items.push(`<li>${blocks(content.join('\n'),depth+1)}</li>`);
        }
        out.push(`<${tag}${ordered&&start!==1?` start="${start}"`:''}>${items.join('')}</${tag}>`);continue;
      }
      // Always consume the current line, including incomplete streamed markup.
      const paragraph=[lines[i++]];
      while(i<lines.length&&lines[i].trim()&&!special(lines[i]))paragraph.push(lines[i++]);
      out.push(`<p>${paragraph.map(line=>inline(line)).join('<br>')}</p>`);
    }
    return out.join('\n');
  }
  globalThis.renderMarkdown=text=>text?blocks(String(text)):'';
})();
