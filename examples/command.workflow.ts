import { defineAsyncWorkflow } from '../muxlio-compiler/src/async.js';

// Both steps use the same language-neutral executor contract. The first
// illustrates an ordinary installed CLI; the second archives its file output.
const { plan } = await defineAsyncWorkflow({
  name: 'command-process-and-archive',
  run: async (ctx) => {
    const processed = await ctx.execute('process-data', {
      executor: 'command',
      params: {
        program: 'node',
        args: [
          '-e',
          "const fs=require('node:fs');const data=JSON.stringify({ready:true});fs.writeFileSync('processed.json',data);fs.writeFileSync(process.env.MUXLIO_OUTPUT_DIR+'/processed.json',data)",
        ],
      },
      outputs: { file: { mime: 'application/json', path: 'processed.json' } },
    });
    const archive = await ctx.execute('archive-result', {
      executor: 'command',
      params: {
        // The small Node wrapper supplies the per-task output path to tar;
        // tar itself still receives discrete argv values without a shell.
        program: 'node',
        args: [
          '-e',
          "const {spawnSync}=require('node:child_process');const path=require('node:path');const r=spawnSync('tar',['-czf',path.join(process.env.MUXLIO_OUTPUT_DIR,'results.tar.gz'),'processed.json'],{cwd:process.cwd(),stdio:'inherit'});process.exit(r.status??1)",
        ],
      },
      outputs: { archive: { mime: 'application/gzip', path: 'results.tar.gz' } },
    });
    return { processed: processed.outputs._result, archive: archive.outputs._result };
  },
});

export default { plan };
